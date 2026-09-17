---
name: electronic-lab-notebook
description: Structured wet-lab experiment recording. Guides through pre- and post-experiment interview (9 sections), saves to project-scoped SQLite, and exports formatted experiment reports. Use when starting, pausing, resuming, or finishing a lab experiment that should be formally recorded and retrievable across sessions.
---
# Electronic Lab Notebook

Guided experiment intake with persistent, retrievable experiment records.

## When to use

- Starting a new experiment and want it formally recorded.
- Saying "record this experiment" or referencing a protocol.
- Finishing an experiment and documenting results.
- Pausing/resuming across sessions.

## 9-section structure

**Pre-experiment (before execution):**
1. Title and Objective
2. Materials (cell lines, reagents, antibodies, kits with catalog/lot numbers)
3. Equipment (instruments, settings, software versions)
4. Protocol (step-by-step, note modifications from published protocols)
5. Expected outcomes (hypothesis confirmation criteria, positive/negative controls)

**Post-experiment (after execution):**
6. Raw data (measurements, images, QC metrics)
7. Observations and deviations (temperature drift, timing errors, equipment issues)
8. Analysis and results (processed data, statistics, figures)
9. Conclusions and next steps

## Workflow

### Step 1: Start experiment

```python
import sqlite3, os
title = input("Experiment title: ").strip()
if not title:
    raise ValueError("Experiment title is required")
db_path = "project_data/lab_notebook.db"
os.makedirs("project_data", exist_ok=True)
conn = sqlite3.connect(db_path)
conn.execute("CREATE TABLE IF NOT EXISTS experiments (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT, date TEXT, status TEXT DEFAULT 'planning', objective TEXT, materials TEXT, equipment TEXT, protocol TEXT, expected_outcomes TEXT, raw_data TEXT, observations TEXT, analysis TEXT, conclusions TEXT, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)")
conn.execute("INSERT INTO experiments (title, date, status) VALUES (?, date('now'), 'planning')", (title,))
exp_id = conn.lastrowid
conn.commit()
```

### Step 2: Update sections interactively

Ask one section at a time via natural conversation. Update as each is filled:

```python
conn.execute("UPDATE experiments SET protocol = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", (protocol_text, exp_id))
conn.commit()
```

### Step 3: Pause and resume

```python
conn.execute("UPDATE experiments SET status = 'paused' WHERE id = ?", (exp_id,))
conn.commit()
rows = conn.execute("SELECT id, title, date FROM experiments WHERE status = 'paused' ORDER BY updated_at DESC").fetchall()
```

### Step 4: Complete and report

```python
conn.execute("UPDATE experiments SET status = 'completed', analysis = ?, conclusions = ? WHERE id = ?", (analysis_text, conclusions_text, exp_id))
conn.commit()
# Generate markdown report and save as artifact
report = f"# Experiment Report\\n\\n## Objective\\n{objective}\\n\\n## Materials\\n{materials}\\n\\n## Protocol\\n{protocol}\\n\\n## Results\\n{analysis}\\n\\n## Conclusions\\n{conclusions}"
report_path = f"experiment_{exp_id}_report.md"
with open(report_path, "w") as f:
    f.write(report)
save_artifacts([report_path])
conn.close()
```

### Step 5: List past experiments

```python
conn = sqlite3.connect(db_path)
rows = conn.execute("SELECT id, title, date, status FROM experiments ORDER BY updated_at DESC LIMIT 10").fetchall()
for r in rows:
    print(f"[{r[3]}] {r[1]} - {r[2]}")
conn.close()
```

## Save artifacts

- experiment_{id}_report.md -- formatted experiment report
- lab_notebook.db -- SQLite database (project persistence)

## Dependencies

Standard library only: sqlite3, os, datetime.
