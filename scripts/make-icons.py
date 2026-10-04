#!/usr/bin/env python3
"""生成 PWA / 桌面图标。

设计思路
--------
图标不能绑死某个品类——产品线会变（酒是现在的主力，以后还有别的），
也不该是"文字图标"（缩到手机桌面的 60px 就糊）。

所以用抽象标记：石墨渐变底 + 香槟金递升柱。
- 纯几何形状，不依赖字体，任何机器生成结果一致
- 表示数据与经营，属于 ERP / 服务类的通用意象
- 单一粗形状，32px 下依然清晰

用法：python3 scripts/make-icons.py
输出：internal/assets/static/icons/ 下的 5 个文件
"""

import math
import os

from PIL import Image, ImageDraw, ImageFilter

OUT = os.path.join(os.path.dirname(__file__), "..",
                   "internal", "assets", "static", "icons")

BG_TOP = (32, 39, 56)        # 石墨偏靛
BG_BOTTOM = (13, 17, 24)     # 近黑
GLOW = (44, 88, 132)         # 底部靛蓝微光
GOLD_TOP = (242, 213, 152)
GOLD_MID = (206, 165, 92)
GOLD_BOTTOM = (166, 122, 52)
SS = 4                       # 超采样倍数


def lerp(a, b, t):
    return tuple(round(x + (y - x) * t) for x, y in zip(a, b))


def background(size, rounded=True, radius_ratio=0.225):
    """石墨渐变底。rounded=False 时铺满整块（maskable / iOS 用）。"""
    w = size * SS
    img = Image.new("RGB", (w, w), BG_BOTTOM)
    d = ImageDraw.Draw(img)
    for y in range(w):
        d.line([(0, y), (w, y)], fill=lerp(BG_TOP, BG_BOTTOM, y / (w - 1)))

    glow = Image.new("RGB", (w, w), (0, 0, 0))
    gd = ImageDraw.Draw(glow)
    gd.ellipse([-w * 0.45, w * 0.62, w * 1.45, w * 1.95], fill=GLOW)
    glow = glow.filter(ImageFilter.GaussianBlur(w * 0.20))
    img = Image.blend(img, glow, 0.20)

    if not rounded:
        return img.convert("RGBA")
    mask = Image.new("L", (w, w), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, w - 1, w - 1],
                                           radius=int(w * radius_ratio), fill=255)
    out = Image.new("RGBA", (w, w), (0, 0, 0, 0))
    out.paste(img, (0, 0), mask)
    return out


def mark_mask(size, height_ratio=0.46):
    """图形标记：三根递升的圆头柱。

    为什么不用字母或酒瓶：
    - 字母要依赖字体，换台机器渲染结果就不一样；手画字母又容易走形
    - 酒瓶绑死了品类（以后还卖别的产品）
    递升柱是纯几何形状：表示数据与经营，属于 ERP/服务类的通用意象，
    不带品类指向，缩到 32px 也不会糊。
    """
    w = size * SS
    mask = Image.new("L", (w, w), 0)
    d = ImageDraw.Draw(mask)

    h = w * height_ratio
    top = (w - h) / 2
    bottom = top + h

    bar_w = h * 0.24                    # 柱宽
    gap = bar_w * 0.52                  # 柱间距
    total_w = bar_w * 3 + gap * 2
    left = (w - total_w) / 2
    radius = bar_w / 2

    # 高度递增：40% / 68% / 100%
    for i, ratio in enumerate((0.40, 0.68, 1.0)):
        x = left + i * (bar_w + gap)
        y = bottom - h * ratio
        d.rounded_rectangle([x, y, x + bar_w, bottom], radius=radius, fill=255)
    return mask


def gold_gradient(size, top_ratio, height_ratio):
    """金色渐变：上浅下深，中间偏香槟。"""
    w = size * SS
    h = w * height_ratio
    top = (w - h) / 2
    grad = Image.new("RGB", (w, w))
    gd = ImageDraw.Draw(grad)
    for y in range(w):
        t = min(1, max(0, (y - top) / h))
        if t < 0.5:
            c = lerp(GOLD_TOP, GOLD_MID, t / 0.5)
        else:
            c = lerp(GOLD_MID, GOLD_BOTTOM, (t - 0.5) / 0.5)
        gd.line([(0, y), (w, y)], fill=c)
    return grad


def build(size, rounded=True, height_ratio=0.46):
    base = background(size, rounded=rounded)
    mask = mark_mask(size, height_ratio)
    layer = Image.new("RGBA", (size * SS, size * SS), (0, 0, 0, 0))
    layer.paste(gold_gradient(size, 0, height_ratio).convert("RGBA"), (0, 0), mask)
    out = Image.alpha_composite(base, layer)
    return out.resize((size, size), Image.LANCZOS)


def main():
    os.makedirs(OUT, exist_ok=True)

    def save(img, name, flat_bg=None):
        path = os.path.join(OUT, name)
        if flat_bg is not None:
            canvas = Image.new("RGB", img.size, flat_bg)
            canvas.paste(img, (0, 0), img)
            canvas.save(path, "PNG", optimize=True)
        else:
            img.save(path, "PNG", optimize=True)
        print(f"  {name:24s} {img.size[0]}×{img.size[1]}  {os.path.getsize(path)/1024:.1f} KB")

    save(build(512), "icon-512.png")
    save(build(192), "icon-192.png")
    save(build(32), "favicon-32.png")

    # maskable：铺满整块，字母收进安全区（80% 直径）内
    save(build(512, rounded=False, height_ratio=0.35), "maskable-512.png")
    # iOS 主屏图标：铺满 + 不透明
    save(build(180, rounded=False), "apple-touch-icon.png", flat_bg=BG_BOTTOM)


if __name__ == "__main__":
    main()
