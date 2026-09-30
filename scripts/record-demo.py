#!/usr/bin/env python3
"""Record the current scripted TUI tour as an asciicast (Unix only).

Run make build first. No LLM credentials, network calls, or real tools are used.
The temporary workspace and home keep local paths and preferences out of the tour.
"""

import argparse
import codecs
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time


def main():
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=root / "bin/qcode")
    parser.add_argument("--output", type=Path, default=root / "docs/assets/demo.cast")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error("qcode binary is missing; run make build first")

    # Let the automatic prompts demonstrate queueing and mocked tool diffs,
    # then show live theme previews, transcript navigation, and model search.
    actions = [
        (3, b"/theme\r"),
        (4, b"\x1b[B"),
        (5, b"\x1b[B"),
        (6, b"\r"),
        (30, b"\x1b[5~"),
        (32, b"\x1b[H"),
        (34, b"\x1b[F"),
        (36, b"/model\r"),
        (38, b"demo-coder-0"),
        (39, b"\x1b[B"),
        (41, b"\x1b"),
        (44, b"/quit\r"),
    ]
    with tempfile.TemporaryDirectory(prefix="qcode-demo-") as temporary:
        home = Path(temporary)
        workspace = home / "workspace/qcode"
        workspace.mkdir(parents=True)
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 36, 120, 0, 0))
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith("QCODE_") and key != "NO_COLOR"}
        environment.update(HOME=str(home), XDG_CONFIG_HOME=str(home / ".config"),
                           XDG_STATE_HOME=str(home / ".local/state"),
                           TERM="xterm-256color", LANG="C.UTF-8", LC_ALL="C.UTF-8")
        process = subprocess.Popen([str(binary), "--demo"], cwd=workspace,
                                   env=environment, stdin=slave, stdout=slave,
                                   stderr=slave, start_new_session=True)
        os.close(slave)
        start = time.monotonic()
        args.output.parent.mkdir(parents=True, exist_ok=True)
        try:
            with args.output.open("w") as recording:
                header = {"version": 2, "width": 120, "height": 36,
                          "timestamp": int(time.time()),
                          "title": "qcode: queues, diffs, themes, and history",
                          "env": {"TERM": "xterm-256color", "SHELL": "/bin/sh"}}
                recording.write(json.dumps(header) + "\n")
                decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
                pending = iter(actions)
                action = next(pending, None)
                while time.monotonic() - start < 50:
                    elapsed = time.monotonic() - start
                    if action and elapsed >= action[0]:
                        os.write(master, action[1])
                        action = next(pending, None)
                    if not select.select([master], [], [], 0.05)[0]:
                        if process.poll() is not None:
                            break
                        continue
                    try:
                        data = os.read(master, 65536)
                    except OSError as error:
                        if error.errno == errno.EIO:
                            break
                        raise
                    if not data:
                        break
                    text = decoder.decode(data)
                    if not text:
                        continue
                    event = [round(time.monotonic() - start, 6), "o", text]
                    recording.write(json.dumps(event, ensure_ascii=False) + "\n")
            if process.wait(timeout=3) != 0:
                raise RuntimeError("demo process failed")
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=3)
            os.close(master)
    print(f"Recorded {args.output}")


if __name__ == "__main__":
    main()
