import json
from unittest.mock import patch

from rotari import Rotari, RotariError
from rotari.client import CommandResult


def completed(stdout="", stderr="", returncode=0):
    return type("Completed", (), {
        "stdout": stdout,
        "stderr": stderr,
        "returncode": returncode,
    })()


def test_add_builds_safe_argv_with_location_options():
    client = Rotari("rotari", basedir="state", project="demo")
    with patch("subprocess.run", return_value=completed()) as run:
        client.add(["./train.sh", "--epochs", "3"], job_name="train", env=["GPU=0"])

    assert run.call_args.args[0] == [
        "rotari", "add", "--basedir", "state", "--project-name", "demo",
        "--env", "GPU=0", "--job-name", "train", "--",
        "./train.sh", "--epochs", "3",
    ]
    assert "shell" not in run.call_args.kwargs
    assert run.call_args.kwargs["check"] is False


def test_show_decodes_machine_readable_output():
    payload = {"run_id": "run-1", "summary": {"status": "finished"}}
    with patch("subprocess.run", return_value=completed(json.dumps(payload))):
        result = Rotari(basedir="state").show("run-1")

    assert result == payload


def test_wait_returns_failed_run_summary_instead_of_raising():
    payload = {"run_id": "run-1", "status": "failed", "exit_code": 2}
    with patch("subprocess.run", return_value=completed(json.dumps(payload), returncode=2)):
        result = Rotari().wait("run-1")

    assert result == payload


def test_command_raises_for_cli_errors():
    with patch("subprocess.run", return_value=completed(stderr="bad option", returncode=1)):
        try:
            Rotari().command("show")
        except RotariError as error:
            assert error.result.returncode == 1
        else:
            raise AssertionError("RotariError was not raised")
