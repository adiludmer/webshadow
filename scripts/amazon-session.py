#!/usr/bin/env python3
"""Drive the scripted Amazon session through the X server.

Input goes through XTEST, never DevTools, so the browser sees ordinary mouse
and keyboard events. Run it against a `webshadow browser` window on an X
display of 1366x900 (see scripts/verify-amazon.sh): it searches for "laptop"
from the homepage, opens the first result, goes back and searches for
"usb c cable". With an output directory it saves a screenshot after each
step (needs ImageMagick's `import`).

    DISPLAY=:99 python3 scripts/amazon-session.py [screenshot dir]

AMAZON_TERMS, a comma-separated list, replaces the two searches with a
longer session: each term is searched, and the first result of every
other one is opened and left with Back. The test fixture was recorded this
way. AMAZON_PACE multiplies every wait; a fast session of many searches
runs into Amazon's "I am human" checkpoint, which this script does not
answer.
"""
import os
import subprocess
import sys
import time

from Xlib import X, XK, display
from Xlib.ext import xtest

# Positions in a 1366x900 window with Chromium's default chrome and the
# --no-sandbox warning bar. FIRST_RESULT is on the first result's image,
# which stays in place whether or not a sponsored banner tops the results.
SEARCH_BOX = (495, 178)
FIRST_RESULT = (400, 820)

PACE = float(os.environ.get("AMAZON_PACE", "1"))

d = display.Display()


def wait(seconds):
    time.sleep(seconds * PACE)

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
    wait(9)


def open_first_result(name):
    click(*FIRST_RESULT)
    wait(9)
    shot(name)
    key("Alt_L+Left")
    wait(5)


wait(12)
shot("1-home")
terms = [t.strip() for t in os.environ.get("AMAZON_TERMS", "").split(",") if t.strip()]
if not terms:
    search("laptop")
    shot("2-search-laptop")
    open_first_result("3-product")
    search("usb c cable")
    shot("4-search-usb-c-cable")
for i, term in enumerate(terms):
    search(term)
    shot("search-%02d" % (i + 1))
    if i % 2 == 0:
        open_first_result("product-%02d" % (i + 1))
