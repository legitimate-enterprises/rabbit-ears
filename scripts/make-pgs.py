"""Writes a minimal, valid PGS (.sup) stream: one white box shown 0.5s-2.0s.

ffmpeg cannot encode PGS, so the fixture script builds the bitmap subtitle
here and muxes it by stream copy.
"""
import struct
import sys

W, H = 96, 24
X, Y = 272, 300  # bottom-center of a 640x360 frame


def seg(kind, pts, body):
    return b"PG" + struct.pack(">IIBH", pts, 0, kind, len(body)) + body


def rle():
    # Color 1 for W pixels per line, then end-of-line.
    run = bytes([0x00, 0x80 | W, 0x01]) if W < 64 else bytes([0x00, 0xC0 | (W >> 8), W & 0xFF, 0x01])
    return (run + b"\x00\x00") * H


def display(pts, objects):
    pcs = struct.pack(">HHBHBBBB", 640, 360, 0x10, 0, 0x80, 0, 0, len(objects))
    if objects:
        pcs += struct.pack(">HBBHH", 0, 0, 0, X, Y)
    out = seg(0x16, pts, pcs)
    out += seg(0x17, pts, struct.pack(">BBHHHH", 1, 0, X, Y, W, H))
    if objects:
        out += seg(0x14, pts, struct.pack(">BB", 0, 0) + bytes([1, 235, 128, 128, 255]))
        data = rle()
        out += seg(0x15, pts, struct.pack(">HBB", 0, 0, 0xC0) + struct.pack(">I", len(data) + 4)[1:]
                   + struct.pack(">HH", W, H) + data)
    return out + seg(0x80, pts, b"")


with open(sys.argv[1], "wb") as f:
    f.write(display(45000, [1]))
    f.write(display(180000, []))
