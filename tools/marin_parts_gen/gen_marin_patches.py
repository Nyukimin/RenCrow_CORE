#!/usr/bin/env python3
"""Marin 疑似Live2D用 ベース+パーツ生成.

fullbody.png から以下を生成する:
- base.png: 目・口を肌で埋め、アホ毛を背景で埋めたベース画像
- parts/eye{L,R}_open.png: 元絵から切り出した開き目パーツ
- parts/eye{L,R}_closed.png / _happy.png: 描画生成した閉じ目パーツ
- parts/mouth_open.png: 元絵から切り出した開き口パーツ
- parts/mouth_closed.png: 描画生成した閉じ口パーツ
- parts/ahoge.png: 元絵から切り出したアホ毛パーツ

Viewer はベースの上にパーツを重ねて表情・瞬き・口パクを合成する。
座標はすべて fullbody.png (400x805) のピクセル座標。

実行: python3 tools/marin_parts_gen/gen_marin_patches.py [デバッグ出力dir]
依存: Pillow
"""
import os
import sys
from PIL import Image, ImageDraw, ImageFilter

SRC = 'internal/adapter/viewer/assets/live2d/marin/images/fullbody.png'
OUT_BASE = 'internal/adapter/viewer/assets/live2d/marin/images/base.png'
OUT_DIR = 'internal/adapter/viewer/assets/live2d/marin/images/parts'
SS = 4  # supersampling

# 領域定義 (x0, y0, x1, y1)
EYE_L = (163, 283, 221, 346)
EYE_R = (253, 276, 317, 338)
MOUTH = (218, 342, 264, 382)
AHOGE = (192, 78, 284, 144)
# アホ毛の「クルリ(輪)」部分のみの輪郭。下辺は頭頂の地肌ラインに沿わせ、
# 根元・地肌はベース側(固定)に残す。
AHOGE_LOOP_POLY = [
    (194, 118), (197, 97), (207, 87), (222, 81), (242, 79), (258, 85),
    (270, 93), (277, 105), (278, 120), (272, 134), (263, 140),
    (256, 139), (248, 136), (238, 133), (228, 133), (220, 136),
    (213, 140), (206, 140), (199, 136), (194, 130),
]
AHOGE_PIVOT = (258, 136)

im = Image.open(SRC).convert('RGB')

SKIN = im.getpixel((210, 357))
SKIN_SHADOW = tuple(max(0, c - 26) for c in SKIN)
LASH = (46, 30, 34)
MOUTH_LINE = (122, 48, 60)


def feather_ellipse_mask(size, inset, blur):
    w, h = size
    m = Image.new('L', (w * SS, h * SS), 0)
    d = ImageDraw.Draw(m)
    d.ellipse((inset * SS, inset * SS, (w - inset) * SS, (h - inset) * SS), fill=255)
    return m.filter(ImageFilter.GaussianBlur(blur * SS)).resize((w, h), Image.LANCZOS)


def qcurve(p0, p1, p2, n=48):
    pts = []
    for i in range(n + 1):
        t = i / n
        x = (1 - t) ** 2 * p0[0] + 2 * (1 - t) * t * p1[0] + t ** 2 * p2[0]
        y = (1 - t) ** 2 * p0[1] + 2 * (1 - t) * t * p1[1] + t ** 2 * p2[1]
        pts.append((x, y))
    return pts


def draw_tapered_line(draw, pts, color, width):
    n = len(pts)
    for i in range(n - 1):
        t = i / (n - 1)
        wgt = width * (0.35 + 0.65 * (1 - abs(2 * t - 1)) ** 0.7)
        draw.line([pts[i], pts[i + 1]], fill=color, width=max(1, int(wgt)))
        r = wgt / 2
        draw.ellipse((pts[i][0] - r, pts[i][1] - r, pts[i][0] + r, pts[i][1] + r), fill=color)


def skin_fill(size):
    """まぶた/口を埋める肌ベース (上部にわずかな影). SSサイズで返す."""
    w, h = size
    W, H = w * SS, h * SS
    layer = Image.new('RGB', (W, H), SKIN)
    shade = Image.new('L', (W, H), 0)
    ds = ImageDraw.Draw(shade)
    ds.ellipse((-W * 0.2, -H * 0.55, W * 1.2, H * 0.45), fill=60)
    shade = shade.filter(ImageFilter.GaussianBlur(6 * SS))
    layer.paste(Image.new('RGB', (W, H), SKIN_SHADOW), (0, 0), shade)
    return layer


def finish(layer_ss, size, inset, blur):
    """スーパーサンプリング層を実寸へ落とし、楕円フェザーの alpha を付ける."""
    w, h = size
    out = layer_ss.resize((w, h), Image.LANCZOS).convert('RGBA')
    out.putalpha(feather_ellipse_mask(size, inset, blur))
    return out


def make_open_part(box):
    """元絵の切り出しパーツ (ベースの肌埋めを完全に覆う広めの alpha)."""
    crop = im.crop(box).convert('RGBA')
    crop.putalpha(feather_ellipse_mask(crop.size, inset=0, blur=1))
    return crop


def make_eyelid(box, happy, flick_dir):
    """閉じ目パーツ: 肌 + まつげアーク."""
    w, h = box[2] - box[0], box[3] - box[1]
    W, H = w * SS, h * SS
    layer = skin_fill((w, h))
    d = ImageDraw.Draw(layer)
    if happy:  # ニコニコ ∩型
        p0 = (W * 0.10, H * 0.60)
        p1 = (W * 0.50, H * 0.30)
        p2 = (W * 0.90, H * 0.60)
    else:  # 通常閉じ ⌄型 (ゆるやか)
        p0 = (W * 0.08, H * 0.52)
        p1 = (W * 0.50, H * 0.68)
        p2 = (W * 0.92, H * 0.54)
    draw_tapered_line(d, qcurve(p0, p1, p2), LASH, 4.4 * SS)
    if flick_dir == 'left':
        fp = qcurve(p0, (p0[0] - W * 0.06, p0[1] + H * 0.02), (p0[0] - W * 0.10, p0[1] + H * 0.10), 16)
    else:
        fp = qcurve(p2, (p2[0] + W * 0.06, p2[1] + H * 0.02), (p2[0] + W * 0.10, p2[1] + H * 0.10), 16)
    draw_tapered_line(d, fp, LASH, 3.2 * SS)
    return finish(layer, (w, h), inset=1, blur=3)


def make_mouth_closed():
    w, h = MOUTH[2] - MOUTH[0], MOUTH[3] - MOUTH[1]
    W, H = w * SS, h * SS
    layer = skin_fill((w, h))
    d = ImageDraw.Draw(layer)
    # にっこり閉じ口 ⌣型
    p0 = (W * 0.18, H * 0.42)
    p1 = (W * 0.50, H * 0.66)
    p2 = (W * 0.82, H * 0.42)
    draw_tapered_line(d, qcurve(p0, p1, p2), MOUTH_LINE, 3.0 * SS)
    # 下唇の淡い影
    lp = qcurve((W * 0.38, H * 0.78), (W * 0.50, H * 0.84), (W * 0.62, H * 0.78), 24)
    draw_tapered_line(d, lp, SKIN_SHADOW, 2.0 * SS)
    return finish(layer, (w, h), inset=1, blur=3)


def luminance(px):
    r, g, b = px[:3]
    return 0.299 * r + 0.587 * g + 0.114 * b


def make_ahoge():
    """アホ毛「クルリ」パーツの切り出しと、ベース用の背景埋め画像.

    AHOGE_LOOP_POLY 内の毛画素のみをパーツにし、地肌・根元はベース側に残す。
    背景はパステルグラデーションなので、各行でマスク外の最近傍画素から
    線形補間して埋める。
    """
    box = AHOGE
    crop = im.crop(box)
    w, h = crop.size
    ref_rows = [crop.getpixel((x, 0)) for x in range(w)]
    # クルリ輪郭ポリゴン (box ローカル座標)
    poly = Image.new('L', (w, h), 0)
    ImageDraw.Draw(poly).polygon(
        [(x - box[0], y - box[1]) for x, y in AHOGE_LOOP_POLY], fill=255)
    # オーバーレイ用: 淡いハロも含む広めマスク
    mask = Image.new('L', (w, h), 0)
    # 塗り潰し用: 確実な暗部(輪郭線)のみ拾い、膨張で毛内部の灰色を覆う。
    # 背景グラデーションの縦変化(±15程度)を誤検出しないよう閾値を分ける。
    solid = Image.new('L', (w, h), 0)
    for y in range(h):
        for x in range(w):
            diff = luminance(ref_rows[x]) - luminance(crop.getpixel((x, y)))
            if diff > 14:
                mask.putpixel((x, y), 255)
            if diff > 35:
                solid.putpixel((x, y), 255)
    mask = mask.filter(ImageFilter.MaxFilter(5))
    # クルリ以外(地肌・頭頂輪郭)はパーツに含めない
    black = Image.new('L', (w, h), 0)
    mask = Image.composite(mask, black, poly)

    # 背景埋め: ポリゴン全域を「純背景アンカー」からの行内補間で塗り潰す。
    # アンカーはポリゴン外かつ十分明るい(=毛でもリボンでもない)画素に限定し、
    # ハロの灰色やリボンの紫を拾わないようにする。
    bg_patch = crop.copy()
    # 輪郭ハロの灰色をアンカーに拾わないよう、ポリゴン近傍6pxを除外する
    poly_far = poly.filter(ImageFilter.MaxFilter(13))
    for y in range(h):
        span = [x for x in range(w) if poly.getpixel((x, y)) > 0]
        if not span:
            continue
        anchors = [x for x in range(w)
                   if poly_far.getpixel((x, y)) == 0
                   and luminance(ref_rows[x]) - luminance(crop.getpixel((x, y))) < 10]
        for x in span:
            left = max((v for v in anchors if v < x), default=None)
            right = min((v for v in anchors if v > x), default=None)
            if left is not None and right is not None:
                t = (x - left) / (right - left)
                cl, cr = crop.getpixel((left, y)), crop.getpixel((right, y))
                c = tuple(int(cl[i] + (cr[i] - cl[i]) * t) for i in range(3))
            elif left is not None:
                c = crop.getpixel((left, y))
            elif right is not None:
                c = crop.getpixel((right, y))
            elif y > 0:
                c = bg_patch.getpixel((x, y - 1))
            else:
                continue
            bg_patch.putpixel((x, y), c)
    # 埋めた領域をぼかして馴染ませる (境界1pxはフェザー)
    blurred = bg_patch.filter(ImageFilter.GaussianBlur(2.0))
    blend_mask = poly.filter(ImageFilter.GaussianBlur(1.0))
    bg_patch.paste(blurred, (0, 0), blend_mask)

    ahoge = crop.convert('RGBA')
    ahoge.putalpha(mask.filter(ImageFilter.GaussianBlur(1.0)))
    return ahoge, bg_patch


def paste_skin(base, box, inset=3, blur=3):
    """ベース側の目/口領域を肌で埋める (パーツより一回り狭い alpha)."""
    w, h = box[2] - box[0], box[3] - box[1]
    fill = skin_fill((w, h)).resize((w, h), Image.LANCZOS).convert('RGBA')
    fill.putalpha(feather_ellipse_mask((w, h), inset, blur))
    base.alpha_composite(fill, (box[0], box[1]))


def main():
    os.makedirs(OUT_DIR, exist_ok=True)

    parts = {
        'eyeL_open': make_open_part(EYE_L),
        'eyeR_open': make_open_part(EYE_R),
        'eyeL_closed': make_eyelid(EYE_L, happy=False, flick_dir='left'),
        'eyeR_closed': make_eyelid(EYE_R, happy=False, flick_dir='right'),
        'eyeL_happy': make_eyelid(EYE_L, happy=True, flick_dir='left'),
        'eyeR_happy': make_eyelid(EYE_R, happy=True, flick_dir='right'),
        'mouth_open': make_open_part(MOUTH),
        'mouth_closed': make_mouth_closed(),
    }
    ahoge, ahoge_bg = make_ahoge()
    parts['ahoge'] = ahoge
    for name, img in parts.items():
        img.save(f'{OUT_DIR}/{name}.png')

    # ベース: 目・口を肌埋め、アホ毛を背景埋め
    base = im.convert('RGBA')
    base.paste(ahoge_bg, (AHOGE[0], AHOGE[1]))
    paste_skin(base, EYE_L)
    paste_skin(base, EYE_R)
    paste_skin(base, MOUTH)
    base.save(OUT_BASE)
    print('wrote', OUT_BASE, 'and', len(parts), 'parts to', OUT_DIR)

    if len(sys.argv) > 1:
        scratch = sys.argv[1]
        # 開きパーツを重ねて元絵と同等に戻ることを確認
        recon = base.copy()
        recon.alpha_composite(parts['eyeL_open'], (EYE_L[0], EYE_L[1]))
        recon.alpha_composite(parts['eyeR_open'], (EYE_R[0], EYE_R[1]))
        recon.alpha_composite(parts['mouth_open'], (MOUTH[0], MOUTH[1]))
        recon.alpha_composite(parts['ahoge'], (AHOGE[0], AHOGE[1]))
        recon.crop((80, 200, 400, 450)).resize((640, 500), Image.LANCZOS).save(f'{scratch}/debug_recon.png')
        # 閉じ目+閉じ口
        blink = base.copy()
        blink.alpha_composite(parts['eyeL_closed'], (EYE_L[0], EYE_L[1]))
        blink.alpha_composite(parts['eyeR_closed'], (EYE_R[0], EYE_R[1]))
        blink.alpha_composite(parts['mouth_closed'], (MOUTH[0], MOUTH[1]))
        blink.alpha_composite(parts['ahoge'], (AHOGE[0], AHOGE[1]))
        blink.crop((80, 200, 400, 450)).resize((640, 500), Image.LANCZOS).save(f'{scratch}/debug_blink.png')
        # ベース素通し (パーツなし)
        base.crop((80, 200, 400, 450)).resize((640, 500), Image.LANCZOS).save(f'{scratch}/debug_base.png')
        print('debug composites written to', scratch)


if __name__ == '__main__':
    main()
