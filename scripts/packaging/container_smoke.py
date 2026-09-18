"""Real isolated container startup, authentication, persistence and restart smoke."""

import argparse
import http.cookiejar
import json
import os
import secrets
import subprocess
import time
import urllib.error
import urllib.request


def docker(*args: str) -> str:
    return subprocess.check_output(["docker", *args], text=True).strip()


def wait_ready(url: str, version: str) -> None:
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url + "/api/health", timeout=3) as response:
                body = json.load(response)
            if body.get("version") != version or body.get("status") != "healthy":
                raise ValueError("Container health version does not match the release")
            return
        except (OSError, urllib.error.URLError):
            time.sleep(0.5)
    raise TimeoutError("Container failed to become ready")


def authenticate(url: str, token: str) -> None:
    cookies = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
    try:
        client.open(url + "/api/go/events/stream?frame_id=package-smoke", timeout=5)
    except urllib.error.HTTPError as error:
        if error.code != 401:
            raise ValueError("Unexpected unauthenticated API status") from error
    else:
        raise ValueError("Authenticated API allowed anonymous access")
    for password, expected in [("invalid-password", 401), (token, 200)]:
        request = urllib.request.Request(
            url + "/login", data=json.dumps({"username": "smoke", "password": password}).encode(),
            headers={"Content-Type": "application/json"},
        )
        try:
            with client.open(request, timeout=10) as response:
                status = response.status
        except urllib.error.HTTPError as error:
            status = error.code
        if status != expected:
            raise ValueError("Container authentication smoke failed")
    if not list(cookies):
        raise ValueError("Login did not establish a session")


def smoke(image: str, version: str) -> None:
    name = "synon-package-smoke-" + secrets.token_hex(6)
    volume = name + "-data"
    token = secrets.token_urlsafe(32)
    docker("volume", "create", volume)
    created = False
    try:
        # The generated smoke credential is passed through the child environment,
        # never through a command-line value or a checked-in configuration.
        environment = {**os.environ, "SYNON_LINK_AUTH_PASSWORD": token}
        subprocess.run(
            ["docker", "run", "--detach", "--name", name, "--publish", "127.0.0.1::8765",
             "--mount", f"type=volume,source={volume},target=/var/lib/synon-biomed",
             "--env", "SYNON_LINK_AUTH_USERNAME=smoke", "--env", "SYNON_LINK_AUTH_PASSWORD", image],
            env=environment, check=True, stdout=subprocess.DEVNULL,
        )
        created = True
        address = docker("port", name, "8765/tcp")
        url = "http://" + address
        wait_ready(url, version)
        with urllib.request.urlopen(url + "/", timeout=10) as response:
            if b"<html" not in response.read().lower():
                raise ValueError("Container does not serve the packaged web client")
        authenticate(url, token)
        docker("exec", name, "sh", "-c", "test $(id -u) -ne 0 && test -w /var/lib/synon-biomed")
        docker("exec", name, "sh", "-c", "printf persistent > /var/lib/synon-biomed/package-smoke")
        docker("restart", name)
        wait_ready(url, version)
        if docker("exec", name, "cat", "/var/lib/synon-biomed/package-smoke") != "persistent":
            raise ValueError("Container state did not survive restart")
        authenticate(url, token)
        print("Container startup, web, authentication, non-root state and restart smoke passed")
    finally:
        if created:
            docker("rm", "--force", name)
        docker("volume", "rm", volume)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    parser.add_argument("version")
    args = parser.parse_args()
    smoke(args.image, args.version)
