"""Minimal demo for the python_venv yoe class.

Invoked on the target by /usr/bin/python-hello, which runs this script
through the python interpreter inside /usr/lib/python-venvs/python-hello.
The pyfiglet import only resolves because that venv's site-packages is on
the interpreter's sys.path -- a regular `python3 -c 'import pyfiglet'`
from the system shell will fail, which is exactly what makes the venv the
unit of distribution.
"""
import sys
from pyfiglet import Figlet


def main(argv):
    text = " ".join(argv[1:]) if len(argv) > 1 else "Hello, yoe!"
    print(Figlet(font="slant").renderText(text))


if __name__ == "__main__":
    main(sys.argv)
