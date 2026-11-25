// @title: Two-Column Research Paper
// @desc: Two-column scientific paper layout with full-width title, abstract, and theorem block
// @category: Academic

#set document(title: "{{title}}", author: "{{author}}")
#set page(paper: "us-letter", margin: (x: 1.6cm, y: 1.8cm), numbering: "1")
#set text(font: "New Computer Modern", size: 9.5pt)
#set heading(numbering: "1.1")
#set par(justify: true, first-line-indent: 1em, leading: 0.55em)
#set math.equation(numbering: "(1)")

#let theorem(name: none, body) = block(
  inset: 6pt,
  stroke: (left: 1.5pt + luma(140)),
  width: 100%,
)[*Theorem#if name != none [ (#name)].* #emph(body)]

#place(top + center, scope: "parent", float: true)[
  #align(center)[
    #text(size: 15pt, weight: "bold")[{{title}}]
    #v(0.5em)
    #text(size: 10pt)[{{author}}] \
    #text(size: 9pt, fill: luma(90))[Affiliation · {{date}}]
    #v(0.8em)
    #block(width: 85%)[
      #set par(first-line-indent: 0pt)
      *Abstract.* Concisely state the objective, methodology, and main result of this paper.
    ]
    #v(0.8em)
  ]
]

#show: rest => columns(2, gutter: 1.2em, rest)

= Introduction

Introduce the problem setting and contributions.

#theorem(name: "Main Bound")[
  For every $x >= 0$, the approximation holds within $epsilon$.
]

= Evaluation

Present quantitative results and discussion.
