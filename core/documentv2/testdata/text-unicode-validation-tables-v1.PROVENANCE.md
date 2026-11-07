# Text Unicode validation tables v1 provenance

`text_unicode_tables_v1.go` freezes the Unicode data used by the
`cutly.text.pinned-font-viewport-px.v2` document contract. Runtime validation does not consult the
Go toolchain's mutable Unicode tables.

- Unicode data version: `17.0.0`
- Go generation toolchain: `go1.27.3`
- Go source tables: `unicode.Latin`, `unicode.Han`, `unicode.Arabic`,
  `unicode.Scripts["Common"]`, `unicode.Scripts["Inherited"]`, `unicode.Cf`, `unicode.Cc`, and
  `unicode.Properties["Variation_Selector"]`
- Normative derived-property source: Unicode 17
  [`DerivedCoreProperties.txt`](https://www.unicode.org/Public/17.0.0/ucd/DerivedCoreProperties.txt),
  dated 2025-07-30; the checked-in `Default_Ignorable_Code_Point` ranges are the exact entries at
  lines 10674–10700 and contain 4,174 code points.
- Generator: `go run gen_text_unicode_tables.go` from `core/documentv2`
- Table profile: `cutly.unicode.text-validation-tables.17.0.0.v1`
- Hash profile: `cutly.hash.sha256.unicode-range-tables-be.v1`
- Table hash: `sha256:92fdc710cee512e805f98b514541910185fdcd9a45bbe022c5d1a7936498af2d`

The hash preimage is: a uint32-big-endian length-prefixed hash-profile string, the same encoding of
the Unicode version, a uint32 table count, then for each table in the fixed order `Latn`, `Hani`,
`Arab`, `Common`, `Inherited`, `Cf`, `Cc`, `VariationSelector`, `DefaultIgnorable`: its
length-prefixed name, uint32 range count, and each `(lo, hi, stride)` as three uint32-big-endian
values.

The Go `unicode` tables are distributed with the Go source under the Go project's BSD-style
license. The underlying Unicode Data Files are redistributed under the following notice.

## Unicode License v3

COPYRIGHT AND PERMISSION NOTICE

Copyright © 1991-2026 Unicode, Inc.

NOTICE TO USER: Carefully read the following legal agreement. BY DOWNLOADING, INSTALLING, COPYING
OR OTHERWISE USING DATA FILES, AND/OR SOFTWARE, YOU UNEQUIVOCALLY ACCEPT, AND AGREE TO BE BOUND BY,
ALL OF THE TERMS AND CONDITIONS OF THIS AGREEMENT. IF YOU DO NOT AGREE, DO NOT DOWNLOAD, INSTALL,
COPY, DISTRIBUTE OR USE THE DATA FILES OR SOFTWARE.

Permission is hereby granted, free of charge, to any person obtaining a copy of data files and any
associated documentation (the "Data Files") or software and any associated documentation (the
"Software") to deal in the Data Files or Software without restriction, including without limitation
the rights to use, copy, modify, merge, publish, distribute, and/or sell copies of the Data Files or
Software, and to permit persons to whom the Data Files or Software are furnished to do so, provided
that either (a) this copyright and permission notice appear with all copies of the Data Files or
Software, or (b) this copyright and permission notice appear in associated Documentation.

THE DATA FILES AND SOFTWARE ARE PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED,
INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE
AND NONINFRINGEMENT OF THIRD PARTY RIGHTS.

IN NO EVENT SHALL THE COPYRIGHT HOLDER OR HOLDERS INCLUDED IN THIS NOTICE BE LIABLE FOR ANY CLAIM,
OR ANY SPECIAL INDIRECT OR CONSEQUENTIAL DAMAGES, OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF
USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING
OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THE DATA FILES OR SOFTWARE.

Except as contained in this notice, the name of a copyright holder shall not be used in advertising
or otherwise to promote the sale, use or other dealings in these Data Files or Software without
prior written authorization of the copyright holder.

Canonical license source: <https://www.unicode.org/license.txt>.
