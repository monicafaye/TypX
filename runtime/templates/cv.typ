// @title: Curriculum Vitae
// @desc: Compact single-page resume / CV with section rules and entry helpers
// @category: Personal

#set page(paper: "a4", margin: (x: 1.6cm, y: 1.5cm))
#set text(size: 10pt)
#set par(leading: 0.55em)

#show heading.where(level: 1): it => block(above: 1em, below: 0.5em)[
  #text(size: 11pt, weight: "bold", fill: rgb("#1d5b79"))[#upper(it.body)]
  #v(-0.4em)
  #line(length: 100%, stroke: 0.6pt + rgb("#1d5b79"))
]

#let entry(role, org, dates, location: "", details) = [
  #grid(
    columns: (1fr, auto),
    [*#role* — #org],
    [#text(fill: luma(90))[#dates #if location != "" [· #location]]],
  )
  #v(0.15em)
  #details
  #v(0.4em)
]

#align(center)[
  #text(size: 20pt, weight: "bold")[{{author}}]
  #v(0.2em)
  #text(size: 9.5pt, fill: luma(80))[#link("mailto:email@example.com")[email\@example.com] · github.com/username · City, Country]
]

= Experience

#entry("Software Engineer", "Organization Name", "2024 – Present", location: "Remote")[
  - Designed and shipped high-impact systems with measurable performance gains.
  - Collaborated across engineering and research teams.
]

= Education

#entry("B.S. in Computer Science", "University Name", "2020 – 2024")[
  - Relevant coursework: Algorithms, Compilers, Distributed Systems.
]

= Skills

- *Languages:* Go, Rust, Python, Typst
- *Tools:* Git, Linux, Docker
