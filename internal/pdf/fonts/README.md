# Embedded statement font

The renderer embeds a subset of Noto Sans SC (SIL Open Font License 1.1;
see OFL.txt). No OS font or remote request is needed to download a statement.

Upstream: https://github.com/google/fonts/tree/e7db6ca603fd4862010383ff17ffd6cc4ffac1c0/ofl/notosanssc
Source file: NotoSansSC[wght].ttf
Source SHA-256: a3041811a78c361b1de50f953c805e0244951c21c5bd412f7232ef0d899af0da

The bundled static font was instantiated with fontTools 4.62.1 at wght=400;
then compressed with gzip (mtime=0). Its uncompressed SHA-256 is
 d65afbfd0d40bedbb2052c185e3051d6ad6611bf525cecc4742e51f9ca2900d5.
The companion compressed metrics derive only from this font's Unicode cmap,
unitsPerEm, hmtx, head and hhea tables and provide coverage checks.

The Go PDF library accepts BMP TrueType cmap entries. Unsupported characters
(including non-BMP characters) are displayed as reversible ASCII Unicode escapes,
with an explicit disclosure in the document. The persisted snapshot retains the
original text. They are never silently removed or rendered as missing-glyph boxes.

Reproduce the font instance with:

    python -m fontTools.varLib.instancer 'NotoSansSC[wght].ttf' wght=400 \
      --output NotoSansSC-Regular.ttf

The font is cached in memory; only used glyphs are embedded in each document.
At most four documents render concurrently. Profile field bounds and the HTTP
response buffer bound keep output and work finite. This dependency is separate
from any optional, externally configured formal tax-invoice provider.
