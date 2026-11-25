// @title: Academic Article
// @desc: Article with title block, abstract, numbered sections, and math setup
// @category: Document

#set document(title: "{{title}}", author: "{{author}}")
#set page(paper: "us-letter", margin: (x: 2.5cm, y: 2.5cm), numbering: "1")
#set text(font: "New Computer Modern", size: 11pt)
#set heading(numbering: "1.1")
#set par(justify: true, leading: 0.65em)

#align(center)[
  #text(size: 18pt, weight: "bold")[{{title}}]

  #v(0.5em)
  {{author}} #h(1em) · #h(1em) {{date}}
]

#v(1em)

#block(inset: (x: 1.5em))[
  *Abstract.* Summarize the problem, method, and primary result here.
]

#v(1em)

= Introduction

Write your introduction here. Inline math like $E = m c^2$ and display equations integrate directly:

$ sum_(i=1)^n x_i = n overline(x) $

= Method

Describe the formulation and experiments.

= Conclusion

Summarize findings and future work.
