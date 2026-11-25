// @title: Presentation Slides
// @desc: Zero-dependency 16:9 slide deck with custom #slide helper and footer
// @category: Presentation

#set page(
  paper: "presentation-16-9",
  margin: (x: 1.8cm, y: 1.4cm),
  footer: context text(size: 11pt, fill: gray)[
    {{title}} #h(1fr) #counter(page).display("1 / 1", both: true)
  ],
)
#set text(size: 20pt)

#let title-slide(title, subtitle: none, author: "{{author}}", date: "{{date}}") = {
  set page(footer: none)
  align(horizon)[
    #text(size: 34pt, weight: "bold")[#title]
    #if subtitle != none [
      #v(0.4em)
      #text(size: 20pt, fill: luma(90))[#subtitle]
    ]
    #v(1.2em)
    #text(size: 16pt, fill: luma(110))[#author · #date]
  ]
}

#let slide(heading-text, body) = {
  pagebreak(weak: true)
  text(size: 26pt, weight: "bold", fill: rgb("#1d5b79"))[#heading-text]
  v(0.2em)
  line(length: 100%, stroke: 1pt + luma(210))
  v(0.6em)
  body
}

#title-slide("{{title}}", subtitle: "Presentation Overview")

#slide("Agenda")[
  - Motivation and goals
  - Architecture and key design choices
  - Results and takeaways
]

#slide("Key Result")[
  $ f(x) = sum_(k=0)^infinity (f^((k))(0))/(k!) x^k $
]
