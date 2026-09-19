from __future__ import annotations

from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import subprocess
from typing import Mapping, Sequence


@dataclass(frozen=True)
class CommandResult:
    """Result of one rotari CLI invocation."""

    args: tuple[str, ...]
    returncode: int
    stdout: str
    stderr: str

    @property
    def run_id(self) -> str | None:
        match = re.search(r"\brun_id=([^\s]+)", self.stdout)
        return match.group(1) if match else None

    def json(self) -> object:
        """Decode the first JSON value printed by the command."""

        return json.loads(self.stdout)


class RotariError(RuntimeError):
    """A rotari command could not be completed."""

    def __init__(self, result: CommandResult):
        self.result = result
        message = result.stderr.strip() or result.stdout.strip() or "rotari command failed"
        super().__init__(f"{message} (exit code {result.returncode})")


class Rotari:
    """Thin, process-based client for the rotari executable.

    The Go CLI remains the source of truth. This class only constructs argv,
    starts the process, and decodes the CLI's machine-readable JSON modes. It
    accepts executable argument lists, not Python functions or closures to
    serialize and submit.
    """

    def __init__(
        self,
        executable: str | os.PathLike[str] = "rotari",
        *,
        basedir: str | os.PathLike[str] | None = None,
        project: str | None = None,
        cwd: str | os.PathLike[str] | None = None,
        env: Mapping[str, str] | None = None,
    ) -> None:
        self.executable = os.fspath(executable)
        self.basedir = os.fspath(basedir) if basedir is not None else None
        self.project = project
        self.cwd = os.fspath(cwd) if cwd is not None else None
        self.env = dict(env) if env is not None else None

    def command(self, *arguments: str, check: bool = True) -> CommandResult:
        """Run an arbitrary rotari subcommand with this client's location."""

        if not arguments:
            raise ValueError("a rotari subcommand is required")
        argv = [self.executable, arguments[0], *self._location_options(), *arguments[1:]]
        result = self._invoke(argv)
        if check and result.returncode != 0:
            raise RotariError(result)
        return result

    def add(
        self,
        command: Sequence[str],
        *,
        executor: str | None = None,
        executor_options: Sequence[str] = (),
        env: Sequence[str] = (),
        job_name: str | None = None,
        depends_on: Sequence[str] = (),
        array: str | None = None,
        run: bool = False,
    ) -> CommandResult:
        arguments = ["add"]
        if executor is not None:
            arguments += ["--executor", executor]
        for option in executor_options:
            arguments += ["--executor-option", option]
        for variable in env:
            arguments += ["--env", variable]
        if job_name is not None:
            arguments += ["--job-name", job_name]
        for dependency in depends_on:
            arguments += ["--depends-on", dependency]
        if array is not None:
            arguments += ["--array", array]
        if run:
            arguments.append("--run")
        arguments += ["--", *command]
        return self.command(*arguments)

    def run(
        self,
        *,
        run_id: str | None = None,
        run_name: str | None = None,
        async_: bool = False,
        failed: bool = False,
        unfinished: bool = False,
        success: bool = False,
        job_ids: Sequence[str] = (),
        overwrite: bool = False,
        partial_array: bool | None = None,
    ) -> CommandResult:
        arguments = ["run"]
        if run_id is not None:
            arguments += ["--run-id", run_id]
        if run_name is not None:
            arguments += ["--run-name", run_name]
        if async_:
            arguments.append("--async")
        if failed:
            arguments.append("--failed")
        if unfinished:
            arguments.append("--unfinished")
        if success:
            arguments.append("--success")
        for job_id in job_ids:
            arguments += ["--job-id", job_id]
        if overwrite:
            arguments.append("--overwrite")
        if partial_array is not None:
            arguments.append(f"--partial-array={str(partial_array).lower()}")
        return self.command(*arguments)

    def retry(self, **options: object) -> CommandResult:
        """Retry failed and unfinished jobs using the CLI retry alias."""

        arguments = ["retry"]
        self._append_run_options(arguments, options)
        return self.command(*arguments)

    def wait(self, run_id: str, *, timeout: str | None = None) -> dict[str, object]:
        arguments = ["wait", "--run-id", run_id, "--json"]
        if timeout is not None:
            arguments += ["--timeout", timeout]
        result = self.command(*arguments, check=False)
        try:
            summary = result.json()
        except json.JSONDecodeError:
            raise RotariError(result) from None
        if not isinstance(summary, dict):
            raise TypeError("rotari wait --json returned a non-object JSON value")
        return summary

    def show(self, run_id: str | None = None) -> dict[str, object]:
        arguments = ["show", "--json"]
        if run_id is not None:
            arguments += ["--run-id", run_id]
        result = self.command(*arguments)
        value = result.json()
        if not isinstance(value, dict):
            raise TypeError("rotari show --json returned a non-object JSON value")
        return value

    def _location_options(self) -> list[str]:
        arguments: list[str] = []
        if self.basedir is not None:
            arguments += ["--basedir", self.basedir]
        if self.project is not None:
            arguments += ["--project-name", self.project]
        return arguments

    def _invoke(self, argv: Sequence[str]) -> CommandResult:
        process = subprocess.run(
            list(argv),
            cwd=self.cwd,
            env=self.env,
            capture_output=True,
            text=True,
            check=False,
        )
        return CommandResult(tuple(argv), process.returncode, process.stdout, process.stderr)

    @staticmethod
    def _append_run_options(arguments: list[str], options: Mapping[str, object]) -> None:
        option_names = {
            "run_id": "--run-id",
            "run_name": "--run-name",
            "async_": "--async",
            "failed": "--failed",
            "unfinished": "--unfinished",
            "success": "--success",
            "overwrite": "--overwrite",
            "partial_array": "--partial-array",
        }
        for name, flag in option_names.items():
            value = options.get(name)
            if value is None:
                continue
            if value is False:
                if name == "partial_array":
                    arguments.append(f"{flag}=false")
                continue
            if value is True:
                arguments.append(flag)
            else:
                arguments += [flag, str(value).lower() if isinstance(value, bool) else str(value)]
        for job_id in options.get("job_ids", ()):
            arguments += ["--job-id", str(job_id)]
