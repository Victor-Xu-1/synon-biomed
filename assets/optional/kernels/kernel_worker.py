"""Synon Biomed scientific worker entry point."""
import sys
from pathlib import Path

# Provider entrypoints execute this file with their own original sys.path.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from synon_biomed_runtime.worker_execution import run

if __name__ == "__main__":
    run()
