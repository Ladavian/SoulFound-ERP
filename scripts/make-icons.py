#!/usr/bin/env python3
"""生成 PWA / 桌面图标。

设计思路
--------
原来的图标是两行 "SOUL FOUND" 文字，512px 下还行，但装到手机桌面只有
60px 左右，两行字会糊成一团。现在改成单一图形标记：

  石墨渐变底（品牌主色）+ 香槟金酒瓶剪影

酒瓶的轮廓在任何尺寸下都一眼认得出，金色在深底上对比度足够。
底部留一道很淡的靛蓝光，避免整块死黑。

画法：先把轮廓画成白色剪影（多个形状叠加），再用它当遮罩铺渐变，
所以拼接处不会出现缝；细节只保留"液面高光"一处，避免小尺寸下变成噪点。

用法：python3 scripts/make-icons.py
输出：internal/assets/static/icons/ 下的 5 个文件
"""

import os
from PIL import Image, ImageDraw, ImageFilter

OUT = os.path.join(os.path.dirname(__file__), "..",
                   "internal", "assets", "static", "icons")

BG_TOP = (30, 37, 53)        # 石墨偏靛
BG_BOTTOM = (13, 17, 24)     # 近黑
GLOW = (44, 88, 132)         # 底部靛蓝微光
GOLD_TOP = (240, 209, 145)   # 香槟金（上）
GOLD_BOTTOM = (168, 124, 52) # 香槟金（下）
SS = 4                       # 超采样倍数


def lerp(a, b, t):
    return tuple(round(x + (y - x) * t) for x, y in zip(a, b))


def background(size, rounded=True, radius_ratio=0.225):
    w = size * SS
    img = Image.new("RGB", (w, w), BG_BOTTOM)
    d = ImageDraw.Draw(img)
    for y in range(w):
        d.line([(0, y), (w, y)], fill=lerp(BG_TOP, BG_BOTTOM, y / (w - 1)))

    glow = Image.new("RGB", (w, w), (0, 0, 0))
    gd = ImageDraw.Draw(glow)
    gd.ellipse([-w * 0.4, w * 0.58, w * 1.4, w * 1.9], fill=GLOW)
    glow = glow.filter(ImageFilter.GaussianBlur(w * 0.18))
    img = Image.blend(img, glow, 0.18)

    if not rounded:
        return img.convert("RGBA")
    mask = Image.new("L", (w, w), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, w - 1, w - 1],
                                           radius=int(w * radius_ratio), fill=255)
    out = Image.new("RGBA", (w, w), (0, 0, 0, 0))
    out.paste(img, (0, 0), mask)
    return out


def smoothstep(t):
    return t * t * (3 - 2 * t)


def bottle_mask(size, height_ratio=0.74):
    """酒瓶剪影：瓶盖 + 瓶颈 + 肩部 + 瓶身。

    肩部用 smoothstep 过渡（S 形曲线），比线性斜边自然，
    不会出现明显的折角。整体细高比例，接近真实酒瓶。
    """
    w = size * SS
    mask = Image.new("L", (w, w), 0)
    d = ImageDraw.Draw(mask)

    h = w * height_ratio
    top = (w - h) / 2
    cx = w / 2

    # 比例：矮胖一点、瓶颈短一点，缩到 32px 时轮廓才清楚
    body_w = h * 0.50
    neck_w = body_w * 0.38
    cap_h = h * 0.055
    neck_h = h * 0.24
    shoulder_h = h * 0.15

    cap_top = top
    neck_top = cap_top + cap_h
    neck_bottom = neck_top + neck_h
    body_top = neck_bottom + shoulder_h
    body_bottom = top + h

    # 瓶盖（略宽于脖颈，上沿微圆）
    d.rounded_rectangle(
        [cx - neck_w * 0.66, cap_top, cx + neck_w * 0.66, neck_top + cap_h * 0.5],
        radius=neck_w * 0.26, fill=255)
    # 瓶颈
    d.rectangle([cx - neck_w / 2, neck_top, cx + neck_w / 2, neck_bottom + 1], fill=255)
    # 肩部：S 形过渡
    steps = 120
    for i in range(steps + 1):
        t = i / steps
        y = neck_bottom + shoulder_h * t
        half = neck_w / 2 + (body_w / 2 - neck_w / 2) * smoothstep(t)
        d.rectangle([cx - half, y, cx + half, y + shoulder_h / steps + 1], fill=255)
    # 瓶身
    d.rounded_rectangle(
        [cx - body_w / 2, body_top - shoulder_h * 0.5, cx + body_w / 2, body_bottom],
        radius=body_w * 0.18, fill=255)
    return mask


def bottle(size, height_ratio=0.74):
    """金色渐变酒瓶 + 一道酒标。

    之前想用"液面高光"表现酒液，小尺寸下会变成一块污渍；
    改成横向酒标：形状规整、意图明确，缩到 32px 也不会糊。
    """
    w = size * SS
    mask = bottle_mask(size, height_ratio)

    h = w * height_ratio
    top = (w - h) / 2
    grad = Image.new("RGB", (w, w))
    gd = ImageDraw.Draw(grad)
    for y in range(w):
        gd.line([(0, y), (w, y)],
                fill=lerp(GOLD_TOP, GOLD_BOTTOM, min(1, max(0, (y - top) / h))))

    layer = Image.new("RGBA", (w, w), (0, 0, 0, 0))
    layer.paste(grad.convert("RGBA"), (0, 0), mask)

    # 酒标：瓶身中下部一条横向色带。
    # 之前想用"液面高光"，小尺寸下会糊成一块污渍；横向酒标形状规整、
    # 意图明确，缩到 32px 也认得出来。
    body_w = h * 0.50
    cx = w / 2
    band_top = top + h * 0.58
    band_bottom = top + h * 0.78
    band = Image.new("RGBA", (w, w), (0, 0, 0, 0))
    bd = ImageDraw.Draw(band)
    bd.rectangle([cx - body_w / 2 - 1, band_top, cx + body_w / 2 + 1, band_bottom],
                 fill=(124, 88, 30, 165))
    # 酒标上下的细亮线，让它看起来是"贴上去的一张纸"
    line_w = max(2, int(w * 0.006))
    bd.rectangle([cx - body_w / 2 - 1, band_top - line_w, cx + body_w / 2 + 1, band_top],
                 fill=(255, 246, 224, 88))
    band.putalpha(Image.composite(band.split()[3], Image.new("L", (w, w), 0), mask))
    layer = Image.alpha_composite(layer, band)
    return layer


def build(size, rounded=True, height_ratio=0.60):
    base = background(size, rounded=rounded)
    mark = bottle(size, height_ratio=height_ratio)
    return Image.alpha_composite(base, mark).resize((size, size), Image.LANCZOS)


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

    # maskable：铺满整块，酒瓶收进安全区（80% 直径）内，避免被系统裁掉
    save(build(512, rounded=False, height_ratio=0.46), "maskable-512.png")
    # iOS 主屏图标：铺满 + 不透明
    save(build(180, rounded=False), "apple-touch-icon.png", flat_bg=BG_BOTTOM)


if __name__ == "__main__":
    main()
