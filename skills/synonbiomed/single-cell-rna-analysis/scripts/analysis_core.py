#!/usr/bin/env python3
"""Reusable Scanpy preprocessing, embedding, and result-table operations."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pandas as pd
import scanpy as sc
from scipy.stats import median_abs_deviation


def quality_control(
    adata,
    *,
    count_mads: float = 5.0,
    gene_mads: float = 5.0,
    mt_mads: float = 3.0,
    max_mt_percent: float = 15.0,
    min_cells_per_gene: int = 10,
    source_is_counts: bool = True,
):
    adata = adata.copy()
    adata.var["mt"] = adata.var_names.str.startswith(("MT-", "mt-"))
    sc.pp.calculate_qc_metrics(adata, qc_vars=["mt"], percent_top=None, log1p=False, inplace=True)
    log_counts = np.log1p(adata.obs["total_counts"].to_numpy())
    log_genes = np.log1p(adata.obs["n_genes_by_counts"].to_numpy())
    mt = adata.obs["pct_counts_mt"].to_numpy()
    pass_mask = (
        _within_mads(log_counts, count_mads)
        & _within_mads(log_genes, gene_mads)
        & _within_mads(mt, mt_mads)
        & (mt <= max_mt_percent)
    )
    adata.obs["pass_qc"] = pass_mask
    filtered = adata[pass_mask].copy()
    sc.pp.filter_genes(filtered, min_cells=min_cells_per_gene)
    if filtered.n_obs == 0 or filtered.n_vars == 0:
        raise ValueError("quality control removed all cells or genes; inspect metrics and choose justified thresholds")
    filtered.layers["counts" if source_is_counts else "source_expression"] = filtered.X.copy()
    return filtered


def embedding_and_clustering(
    adata,
    *,
    batch_key: str | None,
    n_top_genes: int = 2000,
    n_pcs: int = 30,
    leiden_resolution: float = 0.5,
    matrix_kind: str = "counts",
):
    adata = adata.copy()
    if matrix_kind == "counts":
        sc.pp.normalize_total(adata, target_sum=1e4)
        sc.pp.log1p(adata)
    elif matrix_kind == "normalized-linear":
        sc.pp.log1p(adata)
    elif matrix_kind != "log-normalized":
        raise ValueError(f"unsupported matrix_kind {matrix_kind!r}")
    sc.pp.highly_variable_genes(adata, n_top_genes=n_top_genes, flavor="seurat")
    if not bool(adata.var["highly_variable"].any()):
        raise ValueError("highly-variable-gene selection returned no genes")
    model = adata[:, adata.var["highly_variable"]].copy()
    sc.pp.scale(model, max_value=10, zero_center=False)
    effective_pcs = max(2, min(n_pcs, model.n_obs - 1, model.n_vars - 1))
    sc.tl.pca(model, n_comps=effective_pcs)
    representation = "X_pca"
    if batch_key and batch_key in model.obs and model.obs[batch_key].nunique() > 1:
        import harmonypy

        harmony = harmonypy.run_harmony(model.obsm["X_pca"], model.obs, batch_key)
        corrected = np.asarray(harmony.Z_corr)
        if corrected.shape[0] == model.n_obs:
            pass
        elif corrected.shape[1] == model.n_obs:
            corrected = corrected.T
        else:
            raise ValueError(
                f"Harmony returned shape {corrected.shape}; expected one axis to equal n_obs={model.n_obs}"
            )
        model.obsm["X_pca_harmony"] = corrected
        representation = "X_pca_harmony"
    sc.pp.neighbors(model, use_rep=representation)
    sc.tl.umap(model)
    sc.tl.leiden(model, resolution=leiden_resolution, key_added="cluster")
    adata.obs["cluster"] = model.obs["cluster"].astype(str)
    adata.obsm["X_pca"] = model.obsm["X_pca"]
    adata.obsm["X_umap"] = model.obsm["X_umap"]
    if "X_pca_harmony" in model.obsm:
        adata.obsm["X_pca_harmony"] = model.obsm["X_pca_harmony"]
    return adata


def marker_table(adata, groupby: str = "cluster") -> pd.DataFrame:
    if groupby not in adata.obs:
        raise ValueError(f"missing grouping column {groupby!r}")
    sc.tl.rank_genes_groups(adata, groupby=groupby, method="wilcoxon", pts=True)
    groups = list(adata.obs[groupby].astype("category").cat.categories)
    return pd.concat(
        [sc.get.rank_genes_groups_df(adata, group=group).assign(group=str(group)) for group in groups],
        ignore_index=True,
    )


def composition_table(adata, group_columns: list[str], *, population_key: str = "cluster") -> pd.DataFrame:
    columns = [name for name in group_columns if name in adata.obs]
    if "sample_id" in adata.obs and "sample_id" not in columns:
        columns.insert(0, "sample_id")
    if population_key not in adata.obs:
        raise ValueError(f"missing population column {population_key!r}")
    counts = adata.obs.groupby(columns + [population_key], observed=True).size().rename("cell_count").reset_index()
    if columns:
        totals = counts.groupby(columns, observed=True)["cell_count"].transform("sum")
    else:
        totals = counts["cell_count"].sum()
    counts["proportion"] = counts["cell_count"] / totals
    return counts


def save_umap(adata, output: str | Path, color: list[str]) -> None:
    import matplotlib.pyplot as plt

    valid = list(dict.fromkeys(name for name in color if name in adata.obs))
    if not valid:
        valid = ["cluster"]
    columns = min(2, len(valid))
    rows = int(np.ceil(len(valid) / columns))
    with plt.rc_context({"font.size": 9, "axes.titlesize": 12, "legend.fontsize": 8}):
        figure = sc.pl.umap(
            adata,
            color=valid,
            ncols=columns,
            wspace=0.55,
            legend_loc="right margin",
            legend_fontsize=8,
            show=False,
            return_fig=True,
        )
        figure.set_size_inches(8.5 * columns, 6.0 * rows)
        figure.savefig(output, bbox_inches="tight")
    plt.close("all")


def save_qc_metrics(adata, filtered, output: str | Path, *, batch_key: str | None = None) -> None:
    import matplotlib.pyplot as plt

    # The source matrix can be hundreds of gigabytes. Add lightweight QC
    # annotations in place instead of duplicating the expression matrix solely
    # for plotting; the pipeline has already checkpointed the filtered object.
    before = adata
    before.var["mt"] = before.var_names.str.startswith(("MT-", "mt-"))
    if "total_counts" not in before.obs:
        sc.pp.calculate_qc_metrics(before, qc_vars=["mt"], percent_top=None, log1p=False, inplace=True)
    figure, axes = plt.subplots(2, 2, figsize=(12, 8))
    comparisons = (
        ("total_counts", "Total counts per cell"),
        ("n_genes_by_counts", "Detected genes per cell"),
        ("pct_counts_mt", "Mitochondrial reads (%)"),
    )
    for axis, (column, title) in zip(axes.flat[:3], comparisons, strict=True):
        axis.hist(before.obs[column].to_numpy(), bins=60, alpha=0.45, label="before QC")
        axis.hist(filtered.obs[column].to_numpy(), bins=60, alpha=0.65, label="after QC")
        axis.set_title(title)
        axis.set_ylabel("Cells")
        axis.legend(frameon=False)
    retention_axis = axes.flat[3]
    if batch_key and batch_key in before.obs and batch_key in filtered.obs:
        before_counts = before.obs[batch_key].astype(str).value_counts().sort_index()
        after_counts = filtered.obs[batch_key].astype(str).value_counts().reindex(before_counts.index, fill_value=0)
        positions = np.arange(len(before_counts))
        retention_axis.bar(positions - 0.2, before_counts.to_numpy(), width=0.4, label="before QC")
        retention_axis.bar(positions + 0.2, after_counts.to_numpy(), width=0.4, label="after QC")
        retention_axis.set_xticks(positions, before_counts.index, rotation=60, ha="right")
        retention_axis.set_title(f"Retention by {batch_key}")
        retention_axis.set_ylabel("Cells")
        retention_axis.legend(frameon=False)
    else:
        retention_axis.axis("off")
        retention_axis.text(
            0.5,
            0.5,
            f"Cells retained\n{filtered.n_obs:,} / {before.n_obs:,}\n({filtered.n_obs / before.n_obs:.1%})",
            ha="center",
            va="center",
            fontsize=14,
        )
    figure.tight_layout()
    figure.savefig(output, bbox_inches="tight")
    plt.close(figure)


def save_composition_plot(
    composition: pd.DataFrame,
    output: str | Path,
    *,
    population_key: str,
    sample_key: str = "sample_id",
) -> None:
    import matplotlib.pyplot as plt

    if sample_key not in composition or population_key not in composition:
        raise ValueError(f"composition plot requires {sample_key!r} and {population_key!r}")
    pivot = composition.pivot_table(
        index=sample_key,
        columns=population_key,
        values="proportion",
        aggfunc="sum",
        fill_value=0.0,
        observed=True,
    )
    width = max(10.0, min(22.0, 0.55 * len(pivot.index) + 7.0))
    figure, axis = plt.subplots(figsize=(width, 6.5))
    pivot.plot(kind="bar", stacked=True, width=0.86, ax=axis, colormap="tab20")
    axis.set_ylabel("Cell proportion")
    axis.set_xlabel(sample_key)
    axis.set_ylim(0.0, 1.0)
    axis.legend(title=population_key, bbox_to_anchor=(1.02, 1.0), loc="upper left", frameon=False)
    figure.tight_layout()
    figure.savefig(output, bbox_inches="tight")
    plt.close(figure)


def save_marker_evidence_dotplot(
    adata,
    output: str | Path,
    *,
    groupby: str,
    markers: list[str],
) -> None:
    import matplotlib.pyplot as plt

    valid = [marker for marker in dict.fromkeys(markers) if marker in adata.var_names]
    if not valid:
        raise ValueError("marker evidence dot plot has no genes present in the analysis object")
    figure = sc.pl.dotplot(
        adata,
        var_names=valid,
        groupby=groupby,
        standard_scale="var",
        show=False,
        return_fig=True,
    )
    width = max(8.0, min(24.0, 0.45 * len(valid) + 6.0))
    height = max(5.0, min(18.0, 0.38 * adata.obs[groupby].nunique() + 3.0))
    figure.make_figure()
    figure.fig.set_size_inches(width, height)
    figure.savefig(output)
    plt.close("all")


def _within_mads(values: np.ndarray, n_mads: float) -> np.ndarray:
    median = float(np.median(values))
    mad = float(median_abs_deviation(values))
    if mad == 0:
        return np.ones(values.shape, dtype=bool)
    return (values >= median - n_mads * mad) & (values <= median + n_mads * mad)
