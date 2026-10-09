#!/usr/bin/env python3
"""生成 Portway 全套应用图标（Windows 多尺寸 ICO / 托盘 / 应用图 / macOS 图标集）。

背景
----
原始设计稿只有 256×256 一张位图。直接把它双线性缩到 16/24，细笔画会糊成
一团——桌面快捷方式、托盘、安装程序里的图标因此发虚。这里改为：

1. 用「三个圆盘 + 三条圆头连接臂」的参数化模型重建轮廓（对原稿拟合，
   256px 下 IoU ≈ 0.95）；
2. 配色不再用单一线性渐变，而是从原稿采样一张 32×32 的二维颜色场，
   双线性插值取色（原稿的色场并非严格线性，单轴渐变在底部偏绿）；
3. 每个尺寸单独渲染；≤32px 额外收细连接臂、放大中心内点，
   让小尺寸仍能看出「三个圆点 + 中间三角孔」的结构。

依赖：Pillow。仅本机生成图标时需要，不参与构建。

    python scripts/generate_app_icon.py            # 覆盖全部图标资源
    python scripts/generate_app_icon.py --preview  # 只出对照预览图
"""

from __future__ import annotations

import argparse
import math
import os
import struct
import sys
from typing import Iterable

from PIL import Image, ImageDraw

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CLIENT = os.path.join(ROOT, "client")
SOURCE = os.path.join(CLIENT, "assets", "icon_source.png")   # 原始设计稿（256×256）

# ---------------------------------------------------------------- 几何参数
# 坐标空间固定为 256×256（与原稿一致），渲染时按目标尺寸等比缩放。
NODES = {
    "T": (192.2, 52.5),
    "L": (54.6, 132.6),
    "B": (196.9, 210.7),
}
NODE_R = {"T": 51.5, "L": 46.5, "B": 48.1}

# (起点, 终点) -> (起点端半宽, 终点端半宽, 向外弯曲量)
ARMS = {
    ("T", "L"): (21.4, 25.4, 13.6),
    ("L", "B"): (28.0, 28.8, 15.1),
    ("T", "B"): (27.3, 33.3, 13.6),
}

DOT_R = 22.0                    # 节点中心浅色圆半径
DOT_C0 = (0xF8, 0xFB, 0xFC)     # 内点渐变：左上
DOT_C1 = (0xC8, 0xE7, 0xFA)     # 内点渐变：右下

SUPERSAMPLE = 4                 # 轮廓栅格化的超采样倍率
FIELD_GRID = 32                 # 颜色场采样网格
# ≤ 此尺寸改用参数化模型渲染（原稿在这个区间重采样会糊），
# 更大的尺寸直接用原稿重采样，保证与设计稿逐像素一致。
MODEL_MAX = 32

_color_field: list[list[tuple[float, float, float] | None]] | None = None
_source_image: Image.Image | None = None


def source_image() -> Image.Image:
    global _source_image
    if _source_image is None:
        if not os.path.exists(SOURCE):
            raise SystemExit(
                f"缺少原始设计稿：{os.path.relpath(SOURCE, ROOT)}\n"
                "该文件是图标生成的唯一设计来源，请先放回。"
            )
        _source_image = Image.open(SOURCE).convert("RGBA")
    return _source_image


# ---------------------------------------------------------------- 颜色场
def _load_color_field() -> list[list[tuple[float, float, float] | None]]:
    """从原始设计稿采样一张二维色场（排除内点与描边外像素）。"""
    global _color_field
    if _color_field is not None:
        return _color_field
    im = source_image()
    px = im.load()
    w, h = im.size
    g = FIELD_GRID
    acc = [[[0.0, 0.0, 0.0, 0] for _ in range(g)] for _ in range(g)]
    for y in range(h):
        for x in range(w):
            r, gr, b, a = px[x, y]
            if a < 250:
                continue
            if 0.299 * r + 0.587 * gr + 0.114 * b > 185:   # 跳过内点/浅色
                continue
            i = min(g - 1, x * g // w)
            j = min(g - 1, y * g // h)
            c = acc[j][i]
            c[0] += r
            c[1] += gr
            c[2] += b
            c[3] += 1
    grid: list[list[tuple[float, float, float] | None]] = [[None] * g for _ in range(g)]
    for j in range(g):
        for i in range(g):
            c = acc[j][i]
            if c[3]:
                grid[j][i] = (c[0] / c[3], c[1] / c[3], c[2] / c[3])
    # 空格用最近的有效格补齐（形状边缘的格子取不到描边内像素）
    missing = [(i, j) for j in range(g) for i in range(g) if grid[j][i] is None]
    for i, j in missing:
        best, bd = None, 1e9
        for jj in range(g):
            for ii in range(g):
                if grid[jj][ii] is None:
                    continue
                d = (ii - i) ** 2 + (jj - j) ** 2
                if d < bd:
                    bd, best = d, grid[jj][ii]
        grid[j][i] = best
    _color_field = grid
    return grid


def field_color(x: float, y: float) -> tuple[int, int, int]:
    """按 256×256 坐标从色场双线性取色。"""
    grid = _load_color_field()
    g = FIELD_GRID
    fx = x * g / 256.0 - 0.5
    fy = y * g / 256.0 - 0.5
    x0 = math.floor(fx)
    y0 = math.floor(fy)
    tx = fx - x0
    ty = fy - y0
    x0 = max(0, min(g - 1, x0))
    x1 = max(0, min(g - 1, x0 + 1))
    y0 = max(0, min(g - 1, y0))
    y1 = max(0, min(g - 1, y0 + 1))
    c00, c10 = grid[y0][x0], grid[y0][x1]
    c01, c11 = grid[y1][x0], grid[y1][x1]

    def mix(a, b, t):
        return a + (b - a) * t

    out = []
    for ch in range(3):
        top = mix(c00[ch], c10[ch], tx)
        bot = mix(c01[ch], c11[ch], tx)
        out.append(int(round(mix(top, bot, ty))))
    return out[0], out[1], out[2]


# ---------------------------------------------------------------- 形状
def _bezier(a, q, b, t):
    return (
        (1 - t) ** 2 * a[0] + 2 * (1 - t) * t * q[0] + t * t * b[0],
        (1 - t) ** 2 * a[1] + 2 * (1 - t) * t * q[1] + t * t * b[1],
    )


def _bezier_tangent(a, q, b, t):
    return (
        2 * (1 - t) * (q[0] - a[0]) + 2 * t * (b[0] - q[0]),
        2 * (1 - t) * (q[1] - a[1]) + 2 * t * (b[1] - q[1]),
    )


def build_alpha(size: int, thin: float = 0.0) -> Image.Image:
    """栅格化轮廓的 alpha。

    thin > 0 时收细连接臂：16/20/24px 下三条臂与中间三角孔会互相糊死，
    收细一点才能保住「环」的结构。
    """
    s = size * SUPERSAMPLE
    k = s / 256.0
    m = Image.new("L", (s, s), 0)
    d = ImageDraw.Draw(m)

    for name, (cx, cy) in NODES.items():
        r = NODE_R[name] * k
        d.ellipse([cx * k - r, cy * k - r, cx * k + r, cy * k + r], fill=255)

    cx0 = sum(p[0] for p in NODES.values()) / 3
    cy0 = sum(p[1] for p in NODES.values()) / 3

    for (a, b), (w0, w1, bow) in ARMS.items():
        A, B = NODES[a], NODES[b]
        M = ((A[0] + B[0]) / 2, (A[1] + B[1]) / 2)
        dx, dy = B[0] - A[0], B[1] - A[1]
        dist = math.hypot(dx, dy)
        nx, ny = dy / dist, -dx / dist
        if (M[0] - cx0) * nx + (M[1] - cy0) * ny < 0:
            nx, ny = -nx, -ny
        Q = (M[0] + 2 * bow * dist / 155.0 * nx, M[1] + 2 * bow * dist / 155.0 * ny)

        wa, wb = max(4.0, w0 - thin), max(4.0, w1 - thin)
        left, right = [], []
        n = 200
        for i in range(n + 1):
            t = i / n
            w = wa + (wb - wa) * t
            px, py = _bezier(A, Q, B, t)
            tx, ty = _bezier_tangent(A, Q, B, t)
            ln = math.hypot(tx, ty) or 1
            ux, uy = -ty / ln, tx / ln
            left.append(((px + ux * w) * k, (py + uy * w) * k))
            right.append(((px - ux * w) * k, (py - uy * w) * k))
        d.polygon(left + list(reversed(right)), fill=255)
        for t, w in ((0.0, wa), (1.0, wb)):
            px, py = _bezier(A, Q, B, t)
            r = w * k
            d.ellipse([px * k - r, py * k - r, px * k + r, py * k + r], fill=255)

    return m.resize((size, size), Image.LANCZOS)


def build_dot_mask(size: int, dot_boost: float = 0.0) -> Image.Image:
    """节点中心浅色圆的覆盖遮罩（超采样后缩，边缘带抗锯齿）。"""
    s = size * SUPERSAMPLE
    k = s / 256.0
    m = Image.new("L", (s, s), 0)
    d = ImageDraw.Draw(m)
    r = (DOT_R + dot_boost) * k
    for (cx, cy) in NODES.values():
        d.ellipse([cx * k - r, cy * k - r, cx * k + r, cy * k + r], fill=255)
    return m.resize((size, size), Image.LANCZOS)


def render(size: int, thin: float = 0.0, dot_boost: float = 0.0) -> Image.Image:
    silhouette = build_alpha(size, thin)
    k = size / 256.0

    # 主体：从原稿采样的二维颜色场
    body = Image.new("RGB", (size, size))
    bp = body.load()
    for y in range(size):
        sy = (y + 0.5) / k
        for x in range(size):
            bp[x, y] = field_color((x + 0.5) / k, sy)

    # 内点：每个点一组「左上浅 → 右下深」的局部对角渐变
    r_icon = (DOT_R + dot_boost) * k
    dots = Image.new("RGB", (size, size))
    dp = dots.load()
    for y in range(size):
        for x in range(size):
            best = None
            for (cx, cy) in NODES.values():
                ddx = x + 0.5 - cx * k
                ddy = y + 0.5 - cy * k
                if ddx * ddx + ddy * ddy > r_icon * r_icon:
                    continue
                t = (ddx + ddy) / (2.0 * r_icon + 0.5)
                v = min(1.0, max(0.0, (t + 1.0) / 2.0))
                best = v if best is None else min(best, v)
            if best is not None:
                dp[x, y] = tuple(
                    int(round(DOT_C0[i] + (DOT_C1[i] - DOT_C0[i]) * best)) for i in range(3)
                )

    base = body.convert("RGBA")
    base.putalpha(silhouette)
    top = dots.convert("RGBA")
    top.putalpha(build_dot_mask(size, dot_boost))
    return Image.alpha_composite(base, top)


def render_artwork(size: int) -> Image.Image:
    """直接重采样原始设计稿（大于原稿分辨率时按 Lanczos 放大）。"""
    return source_image().resize((size, size), Image.LANCZOS)


def render_size(size: int) -> Image.Image:
    """按尺寸挑渲染方式：小尺寸用模型，大尺寸用原稿。"""
    if size <= MODEL_MAX:
        return render(size, **tune(size))
    return render_artwork(size)


# ---------------------------------------------------------------- ICO 写出
def _bmp_entry(img: Image.Image) -> bytes:
    """32bpp BITMAPINFOHEADER + AND 掩码：资源管理器/安装程序兼容性最好。"""
    w, h = img.size
    px = img.convert("RGBA").load()
    rows = []
    for y in range(h - 1, -1, -1):
        row = bytearray()
        for x in range(w):
            r, g, b, a = px[x, y]
            row += bytes((b, g, r, a))
        rows.append(bytes(row))
    xor = b"".join(rows)
    mask_stride = ((w + 31) // 32) * 4
    mask = bytearray()
    for y in range(h - 1, -1, -1):
        bits = bytearray(mask_stride)
        for x in range(w):
            if px[x, y][3] < 128:
                bits[x >> 3] |= 0x80 >> (x & 7)
        mask += bits
    header = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0, 0, 0, 0, 0, 0)
    return header + xor + bytes(mask)


def _png_entry(img: Image.Image) -> bytes:
    import io

    buf = io.BytesIO()
    img.convert("RGBA").save(buf, format="PNG", optimize=True)
    return buf.getvalue()


def write_ico(path: str, images: Iterable[tuple[int, Image.Image]], png_from: int = 128) -> None:
    entries = []
    for size, img in images:
        entries.append((size, _png_entry(img) if size >= png_from else _bmp_entry(img)))
    header = struct.pack("<HHH", 0, 1, len(entries))
    table = b""
    data = b""
    offset = 6 + 16 * len(entries)
    for size, blob in entries:
        dim = 0 if size >= 256 else size
        table += struct.pack("<BBBBHHII", dim, dim, 0, 0, 1, 32, len(blob), offset)
        offset += len(blob)
        data += blob
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(header + table + data)
    print(f"  {os.path.relpath(path, ROOT)}  [{', '.join(str(s) for s, _ in entries)}]")


# ---------------------------------------------------------------- 尺寸表
WIN_SIZES = [16, 20, 24, 32, 48, 64, 128, 256]
TRAY_SIZES = [16, 20, 24, 32, 48, 64]
MAC_SIZES = [16, 32, 64, 128, 256, 512, 1024]


def tune(size: int) -> dict:
    """小尺寸的额外调整：连接臂收细（thin）+ 内点放大（dot_boost）。"""
    if size <= 16:
        return {"thin": 5.0, "dot_boost": 5.0}
    if size <= 20:
        return {"thin": 3.6, "dot_boost": 3.5}
    if size <= 24:
        return {"thin": 2.2, "dot_boost": 2.0}
    if size <= 32:
        return {"thin": 0.8, "dot_boost": 0.8}
    return {"thin": 0.0, "dot_boost": 0.0}


def make_set(sizes) -> list[tuple[int, Image.Image]]:
    return [(s, render_size(s)) for s in sizes]


def preview(path: str) -> None:
    sets = {f"≤{MODEL_MAX}px（矢量模型 + 小尺寸提示）": [16, 20, 24, 32],
            f">{MODEL_MAX}px（原稿重采样）": [48, 64, 128, 256]}
    tile = 128
    pad = 12
    width = pad + max(len(v) for v in sets.values()) * (tile + pad)
    height = pad + len(sets) * (tile + pad)
    for dark in (False, True):
        sheet = Image.new("RGBA", (width, height),
                          (28, 30, 34, 255) if dark else (248, 249, 251, 255))
        for r, sizes in enumerate(sets.values()):
            for c, s in enumerate(sizes):
                im = render_size(s).resize((tile, tile), Image.NEAREST)
                sheet.alpha_composite(im, (pad + c * (tile + pad), pad + r * (tile + pad)))
        out = path.replace(".png", ("-dark" if dark else "-light") + ".png")
        sheet.convert("RGB").save(out)
        print(f"  {os.path.relpath(out, ROOT)}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--preview", action="store_true", help="只出对照预览，不覆盖图标")
    args = ap.parse_args()

    if args.preview:
        preview(os.path.join(ROOT, "docs", "icon-preview.png"))
        return 0

    print("Windows 图标：")
    write_ico(os.path.join(CLIENT, "windows", "runner", "resources", "app_icon.ico"),
              make_set(WIN_SIZES))
    write_ico(os.path.join(CLIENT, "assets", "tray_icon.ico"), make_set(TRAY_SIZES), png_from=1024)

    print("位图资产：")
    for rel, size in (("client/assets/app_icon.png", 256),
                      ("client/assets/tray_icon.png", 64)):
        render_size(size).save(os.path.join(ROOT, rel))
        print(f"  {rel}  ({size}px)")

    mac = os.path.join(CLIENT, "macos", "Runner", "Assets.xcassets", "AppIcon.appiconset")
    if os.path.isdir(mac):
        print("macOS 图标集：")
        for s in MAC_SIZES:
            render_size(s).save(os.path.join(mac, f"app_icon_{s}.png"))
        print(f"  app_icon_{{{','.join(str(s) for s in MAC_SIZES)}}}.png")

    print("完成。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
