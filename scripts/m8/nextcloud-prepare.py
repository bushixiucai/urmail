#!/usr/bin/env python3
"""Entry point for the non-executing, private Nextcloud rehearsal preparer."""
from pathlib import Path
import runpy

if __name__ == '__main__':
    runpy.run_path(str(Path(__file__).resolve().parents[2] / 'packaging/nextcloud/isolation.py'), run_name='__main__')
