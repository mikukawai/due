#!/usr/bin/env python3
"""Exercise real Gate/Node/MCP binaries against disposable middleware and a local DS stub.

This verifies the DUE control/message path, NOT an actual battle. Never point this
at production middleware: --disposable-middleware is intentionally required.
"""
import argparse
import contextlib
import json
import os
from pathlib import Path
import selectors
import signal
import socket
import socketserver
import subprocess
import sys
import threading
import time
import traceback
import urllib.request
import uuid


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_until(check, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError):
            pass
        time.sleep(0.05)
    raise TimeoutError("fixture readiness deadline exceeded")


def redact(value):
    secret_keys = {"token", "access_token", "refresh_token", "accessToken", "refreshToken",
                   "player_token", "player_tokens", "secret", "jwt_key", "jwt_secret", "client_secret"}
    if isinstance(value, dict):
        return {k: "[redacted]" if k in secret_keys else redact(v) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v) for v in value]
    if isinstance(value, str):
        try:
            decoded = json.loads(value)
        except json.JSONDecodeError:
            return value
        if isinstance(decoded, (dict, list)):
            return json.dumps(redact(decoded), ensure_ascii=False)
    return value


def write_report(path, value):
    with path.open("w", encoding="utf-8") as report:
        os.fchmod(report.fileno(), 0o600)
        json.dump(redact(value), report, ensure_ascii=False, indent=2)


class DSStub(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self):
        self.lock = threading.Lock()
        self.rooms = {}
        self.results = []
        self.actions = []
        super().__init__(("127.0.0.1", 0), DSHandler)

    def command(self, req):
        with self.lock:
            action = req.get("action")
            self.actions.append(action)
            if action == "create_room":
                key = "fixture-" + uuid.uuid4().hex
                room = {"room_key": key, "battle_port": 7000,
                        "player_tokens": [uuid.uuid4().hex, uuid.uuid4().hex],
                        "player_decks": req.get("player_decks"), "metadata": req.get("metadata"), "status": "waiting"}
                self.rooms[key] = room
                return {"ok": True, **room}
            if action == "pull_results":
                results = list(self.results)
                if req.get("clear"):
                    self.results.clear()
                return {"ok": True, "results": results}
            if action == "list_rooms":
                return {"ok": True, "rooms": list(self.rooms.values())}
            if action == "ping":
                return {"ok": True, "service": "isolated-ds-stub", "room_count": len(self.rooms)}
            return {"ok": False, "error": "unsupported fixture action"}

    def complete(self, key, winner=0):
        with self.lock:
            room = self.rooms.pop(key)
            self.results.append({"room_key": key, "winner_id": winner, "reason": "fixture_completion",
                                 "status": "finished", "player_decks": room["player_decks"]})
            return room


class DSHandler(socketserver.StreamRequestHandler):
    def handle(self):
        request = json.loads(self.rfile.readline(1024 * 1024))
        self.wfile.write((json.dumps(self.server.command(request)) + "\n").encode())


class MCP:
    CLOSE_TIMEOUT = 5

    def __init__(self, executable, cwd, transcript):
        self.err = None
        self.proc = None
        self.selector = None
        self._closed = False
        self.buf = b""
        self.counter = 0
        self.transcript = transcript
        self.clients = []
        try:
            self.err = (cwd / "mcp.stderr.log").open("w")
            self.proc = subprocess.Popen([str(executable)], cwd=cwd, stdin=subprocess.PIPE,
                                         stdout=subprocess.PIPE, stderr=self.err)
            self.selector = selectors.DefaultSelector()
            self.selector.register(self.proc.stdout, selectors.EVENT_READ)
            self.rpc("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                     "clientInfo": {"name": "due-controlled-upgrade-smoke", "version": "1"}})
            self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        except BaseException as exc:
            try:
                self.close()
            except Exception as cleanup_exc:
                note = "MCP constructor cleanup failed: " + str(cleanup_exc)
                add_note = getattr(exc, "add_note", None)
                if callable(add_note):
                    add_note(note)
                else:
                    print(note, file=sys.stderr)
            raise

    def send(self, obj):
        self.transcript.append({"request": redact(obj)})
        self.proc.stdin.write((json.dumps(obj) + "\n").encode())
        self.proc.stdin.flush()

    def rpc(self, method, params, timeout=20):
        self.counter += 1
        identifier = self.counter
        self.send({"jsonrpc": "2.0", "id": identifier, "method": method, "params": params})
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            while b"\n" in self.buf:
                line, self.buf = self.buf.split(b"\n", 1)
                if not line.strip():
                    continue
                obj = json.loads(line)
                self.transcript.append({"response": redact(obj)})
                if obj.get("id") != identifier:
                    continue
                if "error" in obj:
                    raise RuntimeError("MCP JSON-RPC error: " + json.dumps(obj["error"]))
                return obj["result"]
            if not self.selector.select(max(0, deadline-time.monotonic())):
                break
            chunk = os.read(self.proc.stdout.fileno(), 65536)
            if not chunk:
                raise RuntimeError("MCP exited before response")
            self.buf += chunk
        raise TimeoutError("MCP request timed out: " + method)

    def tool(self, name, **arguments):
        result = self.rpc("tools/call", {"name": name, "arguments": arguments})
        if result.get("isError"):
            raise RuntimeError("MCP tool failed: " + name + " " + json.dumps(result.get("content")))
        if "structuredContent" in result:
            return result["structuredContent"]
        return json.loads(next(x["text"] for x in result["content"] if x["type"] == "text"))

    def request(self, client, route, data=None):
        response = self.tool("request_protocol", client_id=client, route=route,
                             data=data or {}, timeout_ms=10000)["response"]["data"]
        assert isinstance(response, dict), "MCP lost the received JSON body"
        return response

    def message(self, client, route):
        data = self.tool("wait_message", client_id=client, route=route, timeout_ms=10000)["message"]["data"]
        assert isinstance(data, dict), "MCP push body is empty"
        return data

    def close(self):
        if self._closed:
            return
        errors = []

        def cleanup(label, action):
            try:
                action()
            except Exception as exc:
                errors.append(label + ": " + type(exc).__name__ + ": " + str(exc))

        try:
            for client in self.clients:
                with contextlib.suppress(Exception):
                    self.rpc("tools/call", {"name": "disconnect_client", "arguments": {"client_id": client}},
                             timeout=self.CLOSE_TIMEOUT)
            self.clients.clear()
            if self.proc is not None:
                if self.proc.stdin is not None:
                    cleanup("stdin EOF", self.proc.stdin.close)
                for stage, stop in (("EOF", None), ("terminate", self.proc.terminate), ("kill", self.proc.kill)):
                    if stop is not None:
                        cleanup(stage, stop)
                    try:
                        self.proc.wait(timeout=self.CLOSE_TIMEOUT)
                        break
                    except subprocess.TimeoutExpired as exc:
                        if stage == "kill":
                            errors.append("wait after kill: " + str(exc))
                    except Exception as exc:
                        errors.append("wait after " + stage + ": " + type(exc).__name__ + ": " + str(exc))
        finally:
            if self.selector is not None:
                cleanup("selector close", self.selector.close)
            if self.proc is not None:
                for label, pipe in (("stdin", self.proc.stdin), ("stdout", self.proc.stdout), ("stderr", self.proc.stderr)):
                    if pipe is not None and not pipe.closed:
                        cleanup(label + " close", pipe.close)
            if self.err is not None:
                cleanup("stderr log close", self.err.close)
        self._closed = not errors
        if errors:
            raise RuntimeError("MCP cleanup failed: " + "; ".join(errors))


def make_config(repo, work, application, redis, consul, ws_port, ds_port, prefix):
    dest = work / application
    (dest / "etc").mkdir(parents=True, exist_ok=True)
    (dest / "log").mkdir(exist_ok=True)
    source = (repo / application / "etc/etc.toml").read_text()
    source = source.replace('mode = "debug"', 'mode = "release"')
    source = source.replace('shutdownMaxWaitTime = "0s"', 'shutdownMaxWaitTime = "5s"')
    source = source.replace('addr = ":0"', 'addr = "127.0.0.1:0"')
    source = source.replace('addr = ":3553"', 'addr = "127.0.0.1:' + str(ws_port) + '"')
    source = source.replace('127.0.0.1:6379', redis).replace('127.0.0.1:8500', consul)
    source = source.replace('prefix = "due"', 'prefix = ' + json.dumps(prefix))
    source = source.replace('healthCheck = true', 'healthCheck = false')
    source = source.replace('heartbeatCheck = true', 'heartbeatCheck = false')
    source = source.replace('ds_address = "100.114.203.26:7001"', 'ds_address = "127.0.0.1:' + str(ds_port) + '"')
    source = source.replace('ds_external_host = "100.114.203.26"', 'ds_external_host = "127.0.0.1"')
    (dest / "etc/etc.toml").write_text(source)
    return dest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--consul-addr", required=True)
    parser.add_argument("--redis-addr", required=True)
    parser.add_argument("--work-dir", type=Path, required=True)
    parser.add_argument("--disposable-middleware", action="store_true", required=True)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    work = args.work_dir.resolve()
    work.mkdir(mode=0o700, parents=True, exist_ok=False)
    summary = {"passed": False, "ds_mode": "localhost stub; no real battle exercised", "checks": [],
               "known_baseline_issues": [], "work_dir": str(work)}
    transcript = []
    processes = []
    mcp = None
    ds = DSStub()
    ds_thread = threading.Thread(target=ds.serve_forever, daemon=True)
    ds_thread.start()
    ws_port = free_port()
    prefix = "due_upgrade_" + uuid.uuid4().hex
    try:
        for application in ("node", "gate", "mcp-client"):
            make_config(repo, work, application, args.redis_addr, args.consul_addr, ws_port, ds.server_address[1], prefix)
        for application in ("node", "gate"):
            cwd = work / application
            output = (cwd / "process.log").open("w")
            environment = {**os.environ, "DUE_ETC": str(cwd / "etc"), "GOMAXPROCS": "2"}
            process = subprocess.Popen([str(args.bin_dir / application)], cwd=cwd, env=environment,
                                       stdout=output, stderr=subprocess.STDOUT)
            processes.append((application, process, output))
        def registered():
            data = json.load(urllib.request.urlopen("http://" + args.consul_addr + "/v1/agent/services", timeout=2))
            return len(data) == 2
        wait_until(registered)
        wait_until(lambda: socket.create_connection(("127.0.0.1", ws_port), timeout=0.2).close() is None)
        summary["checks"].append("real Gate and Node registered in disposable Consul")
        mcp = MCP(args.bin_dir / "mcp-client", work / "mcp-client", transcript)
        tools = mcp.rpc("tools/list", {})["tools"]
        assert len(tools) == 19
        summary["checks"].append("real MCP stdio initialization and 19 tools")
        clients = []
        credentials = []
        for uid in (910001, 910002):
            client = mcp.tool("connect_client", label="fixture-" + str(uid), addr="ws://127.0.0.1:" + str(ws_port))["client_id"]
            mcp.clients.append(client)
            clients.append(client)
            token = mcp.tool("generate_token", uid=uid)["access_token"]
            response = mcp.request(client, 1001, {"token": token})
            assert response["code"] == 0 and response["uid"] == uid
            assert response["player"]["nickname"] == "Player_" + str(uid)
            credentials.append(response)
        a, b = clients
        summary["checks"].append("two real clients logged in through WS/Gate/Node with nonempty MCP bodies")
        greet = mcp.request(a, 1, {"message": "controlled-upgrade-smoke"})
        assert greet["code"] == 0 and "server" in greet["message"]
        summary["checks"].append("greet response payload decoded")
        refreshed = mcp.request(a, 1004, {"token": credentials[0]["refreshToken"]})
        assert refreshed["code"] == 0 and refreshed["accessToken"] and refreshed["refreshToken"]
        summary["checks"].append("refresh-token request and response")
        mcp.tool("send_protocol", client_id=a, route=2001, data={"deck_id": "fixture-deck-a"})
        # This is a sequential queue-path regression, not a concurrent queue stress test.
        time.sleep(0.15)
        matched = mcp.tool("quick_match", client_id=b, deck_id="fixture-deck-b", timeout_ms=10000)["response"]["data"]
        assert matched["code"] == 0
        ready_a, ready_b = mcp.message(a, 2003), mcp.message(b, 2003)
        key = ready_a["room_key"]
        assert key == ready_b["room_key"] == matched["room_key"]
        assert {ready_a["player_id"], ready_b["player_id"]} == {0, 1}
        with ds.lock:
            room = dict(ds.rooms[key])
        assert ready_a["player_token"] == room["player_tokens"][ready_a["player_id"]]
        assert ready_b["player_token"] == room["player_tokens"][ready_b["player_id"]]
        if "player_id" not in matched:
            summary["known_baseline_issues"].append("player_id=0 omitted from direct response (existing omitempty)")
        summary["checks"].append("quick match created one DS-stub room and delivered correct distinct player tokens in both 2003 pushes")
        assert any(r["room_key"] == key for r in mcp.request(a, 2004)["rooms"])
        assert mcp.request(b, 2005, {"room_key": key})["room_key"] == key
        summary["checks"].append("spectate list and join control responses")
        room = ds.complete(key)
        winner = room["metadata"]["uid1"]
        for client in (a, b):
            result = mcp.message(client, 2006)
            assert result["winner_uid"] == winner and result["room_key"] == key
        for client, uid in zip((a, b), (910001, 910002)):
            stats = mcp.request(client, 2011)
            assert stats["code"] == 0
            assert stats.get("wins", 0) == int(uid == winner)
            assert stats.get("losses", 0) == int(uid != winner)
        summary["checks"].append("DS-stub result polled by real Node, both 2006 pushes received, real in-memory stats updated")
        for client in (a, b):
            mcp.tool("poll_messages", client_id=client)
            mcp.tool("send_protocol", client_id=client, route=2001, data={"deck_id": "fixture-cancel"})
            time.sleep(0.15)
            assert mcp.request(client, 2002)["code"] == 0
            assert mcp.message(client, 2001)["code"] == 1
        summary["checks"].append("queued match cancellation acknowledged and pending requests returned Canceled")
        code = mcp.request(a, 2008, {"deck_id": "fixture-invite-a"})["invite_code"]
        joined = mcp.request(b, 2009, {"invite_code": code, "deck_id": "fixture-invite-b"})
        invite_a, invite_b = mcp.message(a, 2003), mcp.message(b, 2003)
        assert invite_a["room_key"] == invite_b["room_key"] == joined["room_key"]
        assert invite_a["player_id"] == 0 and invite_b["player_id"] == 1
        assert invite_a["player_token"] != invite_b["player_token"]
        if joined["player_token"] == invite_a["player_token"]:
            summary["known_baseline_issues"].append("2009 direct response still contains creator identity/token; existing business bug, not changed by compatibility upgrade")
        summary["checks"].append("invite creation/join and both distinct-identity 2003 pushes (direct-response baseline defect recorded separately)")
        invalid = mcp.request(b, 2009, {"invite_code": "NO_SUCH_INVITE", "deck_id": "fixture-invalid"})
        assert invalid["code"] == 5
        summary["checks"].append("invalid invite returns NotFound")
        mcp.tool("disconnect_client", client_id=a)
        reconnected = mcp.tool("connect_client", label="fixture-reconnected", addr="ws://127.0.0.1:" + str(ws_port))["client_id"]
        mcp.clients.append(reconnected)
        reconnect = mcp.request(reconnected, 1002, {"token": refreshed["accessToken"]})
        assert reconnect["code"] == 0 and reconnect["uid"] == 910001
        assert mcp.request(reconnected, 1003)["code"] == 0
        summary["checks"].append("disconnect, new-connection token reconnect and logout")
        summary["passed"] = True
    except Exception as exc:
        summary["failure"] = type(exc).__name__ + ": " + str(exc)
        summary["traceback"] = traceback.format_exc()
    finally:
        def teardown_warning(label, exc):
            summary["passed"] = False
            summary.setdefault("teardown_warnings", []).append(label + ": " + type(exc).__name__ + ": " + str(exc))

        def cleanup(label, action):
            try:
                action()
            except Exception as exc:
                teardown_warning(label, exc)

        if mcp is not None:
            cleanup("MCP close", mcp.close)
        for name, process, output in reversed(processes):
            cleanup(name + " terminate", lambda: process.send_signal(signal.SIGTERM))
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                cleanup(name + " kill", process.kill)
                cleanup(name + " wait after kill", lambda: process.wait(timeout=5))
                summary.setdefault("teardown_warnings", []).append(name + " required forced termination")
            except Exception as exc:
                teardown_warning(name + " wait", exc)
            cleanup(name + " log close", output.close)
        cleanup("DS shutdown", ds.shutdown)
        cleanup("DS server close", ds.server_close)
        cleanup("DS thread join", lambda: ds_thread.join(timeout=3))
        if ds_thread.is_alive():
            teardown_warning("DS thread join", TimeoutError("DS thread is still running"))
        summary["ds_actions"] = sorted(set(ds.actions))
        cleanup("transcript report", lambda: write_report(work / "mcp-transcript.json", transcript))
        cleanup("summary report", lambda: write_report(work / "summary.json", summary))
        print(json.dumps(redact(summary), ensure_ascii=False, indent=2))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
