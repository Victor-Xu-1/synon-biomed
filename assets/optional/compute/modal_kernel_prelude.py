import hashlib
import modal
import os

app = modal.App.lookup(__SYNON_APP_NAME__, create_if_missing=True)
_BOUND_CONFIG_HASH = __SYNON_CONFIG_HASH__
_PROVIDER_ID = __SYNON_PROVIDER_ID__
_ENVS = os.environ.get("OPERON_BYOC_ENVS_DIR")
_OP_TAGS = {
    key: value
    for key, value in (
        ("synon-biomed-install-id", os.environ.get("OPERON_BYOC_INSTALL_ID", "")),
        ("synon-biomed-org", os.environ.get("OPERON_BYOC_ORG_ID", "")),
        ("synon-biomed-frame", os.environ.get("OPERON_BYOC_FRAME_ID", "")),
    )
    if value
}
def _load_env(path, name):
    source = open(path, "rb").read()
    namespace = {"modal": modal, "__name__": name}
    exec(compile(source, path, "exec"), namespace)
    return source, namespace


def list_envs():
    result = {}
    if not _ENVS:
        return {"_envs_dir": None}
    for filename in sorted(os.listdir(_ENVS)):
        if not filename.endswith(".py") or filename.startswith("_"):
            continue
        try:
            _, namespace = _load_env(f"{_ENVS}/{filename}", filename[:-3])
            result[filename[:-3]] = namespace.get("META", {})
        except Exception as exc:
            result[filename[:-3]] = {"packages": [], "_error": repr(exc)}
    result["_envs_dir"] = _ENVS
    return result


def credentials_get(name):
    value = host.credentials.get(name)
    return value.get("value") if isinstance(value, dict) and "value" in value else value


class ComputeProviderConfigStale(RuntimeError):
    pass


def compute_provider_config():
    config = host.compute.config_get(_PROVIDER_ID) or {}
    live = config.get("config_hash")
    if live != _BOUND_CONFIG_HASH:
        raise ComputeProviderConfigStale(
            "compute provider settings changed after this kernel started; "
            "close this provider kernel and open a fresh one"
        )
    return config


def build_env(name, *, path=None, secrets=None, hydrate=False):
    compute_provider_config()
    source, namespace = _load_env(path or f"{_ENVS}/{name}.py", name)
    metadata = namespace.get("META") or {}
    required = metadata.get("needs_secrets", [])
    if secrets is None:
        secrets = {key: credentials_get(key) for key in required}
    missing = [key for key in required if not secrets.get(key)]
    if missing:
        raise ValueError(f"{name}: missing credentials {missing}")
    with modal.enable_output():
        image, volumes, environment = namespace["build"](secrets=secrets)
        image.build(app)
    image_id = image.object_id
    volume_name = lambda value: getattr(value, "_name", None) or getattr(value, "name", None)
    result = {
        "image": image_id,
        "spec_sha": hashlib.sha256(source).hexdigest()[:16],
        "volumes": {mount: volume_name(volume) for mount, volume in volumes.items()},
        "env": environment,
        "gpu_default": metadata.get("gpu_default"),
        "packages": metadata.get("packages", []),
        "egress_domains": metadata.get("egress_domains", []),
        "hydrate_defined": namespace.get("HYDRATE") is not None,
        "hydrated": False,
    }
    command = namespace.get("HYDRATE")
    if hydrate and command is None:
        result["hydrate_error"] = "hydrate=True but env defines no HYDRATE"
        return result
    if hydrate and command:
        sandbox = None
        tail = []
        try:
            sandbox = modal.Sandbox.create(
                "sleep",
                "1800",
                image=image,
                app=app,
                timeout=1800,
                volumes=volumes,
                **({"tags": _OP_TAGS} if _OP_TAGS else {}),
            )
            import shlex

            process = sandbox.exec("bash", "-c", shlex.join(command) + " 2>&1")
            for line in process.stdout:
                tail.append(line.rstrip())
                tail[:] = tail[-40:]
                print(f"[hydrate {name}]", line.rstrip(), flush=True)
            return_code = process.wait()
            if return_code != 0:
                result["hydrate_error"] = (
                    f"rc={return_code}; tail: " + "\n".join(tail[-20:])
                )
            else:
                result["hydrated"] = True
        except Exception as exc:
            result["hydrate_error"] = repr(exc)
        finally:
            if sandbox is not None:
                try:
                    sandbox.terminate()
                except Exception:
                    pass
    return result
