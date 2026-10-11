#!/usr/bin/env python3
"""Drive the scripted Amazon session through the X server.

Input goes through XTEST, never DevTools, so the browser sees ordinary mouse
and keyboard events. Run it against a `webshadow browser` window on an X
display of 1366x900 (see scripts/verify-amazon.sh): it searches for "laptop"
from the homepage, opens the first result, goes back and searches for
"usb c cable". With an output directory it saves a screenshot after each
step (needs ImageMagick's `import`).

    DISPLAY=:99 python3 scripts/amazon-session.py [screenshot dir]
"""
import os
import subprocess
import sys
import time

from Xlib import X, XK, display
from Xlib.ext import xtest

# Positions in a 1366x900 window with Chromium's default chrome and the
# --no-sandbox warning bar.
SEARCH_BOX = (495, 178)
FIRST_RESULT = (760, 700)

d = display.Display()
shots = sys.argv[1] if len(sys.argv) > 1 else ""


def keycode(name):
    return d.keysym_to_keycode(XK.string_to_keysym(name))


def key(combo):
    *mods, name = combo.split("+")
    for m in mods:
        xtest.fake_input(d, X.KeyPress, keycode(m))
    xtest.fake_input(d, X.KeyPress, keycode(name))
    xtest.fake_input(d, X.KeyRelease, keycode(name))
    for m in reversed(mods):
        xtest.fake_input(d, X.KeyRelease, keycode(m))
    d.sync()
    time.sleep(0.05)


def type_text(text):
    for ch in text:
        key("space" if ch == " " else ch)
        time.sleep(0.08)


def click(x, y):
    xtest.fake_input(d, X.MotionNotify, x=x, y=y)
    d.sync()
    time.sleep(0.2)
    xtest.fake_input(d, X.ButtonPress, 1)
    xtest.fake_input(d, X.ButtonRelease, 1)
    d.sync()


def shot(name):
    if shots:
        os.makedirs(shots, exist_ok=True)
        subprocess.run(["import", "-window", "root", os.path.join(shots, name + ".png")], check=False)


def search(term):
    click(*SEARCH_BOX)
    key("Control_L+a")
    type_text(term)
    key("Return")
    time.sleep(9)


time.sleep(12)
shot("1-home")
search("laptop")
shot("2-search-laptop")
click(*FIRST_RESULT)
time.sleep(9)
shot("3-product")
key("Alt_L+Left")
time.sleep(5)
search("usb c cable")
shot("4-search-usb-c-cable")
