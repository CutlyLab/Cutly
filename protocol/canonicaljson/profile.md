# Cutly canonical integer JSON v1

`cutly.json.canonical-integer.v1` is the byte identity profile used by public
Go/TypeScript protocols. It is deliberately narrower than generic JSON.

- Input is valid UTF-8 and contains exactly one JSON value.
- Strings are Unicode scalar sequences. Unpaired UTF-16 surrogate escapes fail.
- Decoded duplicate object member names fail, including escape aliases.
- Object names sort lexicographically by UTF-16 code units. Arrays retain order.
- Numbers use integer syntax only. Fractions and exponents fail; `-0` becomes `0`.
- Strings use UTF-8 directly, short escapes for JSON controls, lowercase `\u00xx`
  for the remaining controls, and no optional slash or HTML escaping.
- Whitespace outside strings is removed. Unknown members are retained; the
  owning protocol schema decides whether they are legal.
- NaN, infinities, invalid UTF-8, invalid escapes, duplicate names, trailing
  data and resource-limit exhaustion fail without publishing bytes or a digest.

Protocol schemas remain responsible for signed/unsigned bounds. Implementations
must not round an admitted integer through an IEEE-754 number; JavaScript
bindings parse and write the original integer token.
