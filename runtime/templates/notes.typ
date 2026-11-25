// @title: Lecture & Study Notes
// @desc: Clean study notes with callout boxes, equation numbering, and header
// @category: Notes

#set page(
  paper: "a4",
  margin: 2cm,
  header: align(right)[#emph[#text(fill: gray)[{{title}} — {{date}}]]],
)
#set text(size: 10.5pt)
#set heading(numbering: "1.1")
#set math.equation(numbering: "(1)")

#let note(title, body) = block(
  fill: luma(245),
  stroke: (left: 2pt + rgb("#239dad")),
  inset: 10pt,
  radius: (right: 4pt),
  width: 100%,
)[*#title.* #body]

= {{title}}

#text(fill: gray)[Author: {{author}} · Date: {{date}}]

#v(0.5em)

== Key Ideas

- Core concept one
- Core concept two

#note("Definition")[
  State key definitions or theorems concisely here.
]

== Derivations & Examples

$ integral_a^b f'(x) dif x = f(b) - f(a) $
