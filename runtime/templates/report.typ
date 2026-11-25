// @title: Technical Report
// @desc: Multi-section report with cover block, table of contents, and headers
// @category: Document

#set document(title: "{{title}}", author: "{{author}}")
#set page(
  paper: "a4",
  margin: (top: 2.5cm, bottom: 2.5cm, x: 2.2cm),
  numbering: "1 / 1",
  header: context {
    if counter(page).get().first() > 1 [
      #text(size: 9pt, fill: gray)[{{title}} #h(1fr) {{date}}]
    ]
  },
)
#set text(size: 10.5pt)
#set heading(numbering: "1.1")
#set par(justify: true)

#v(2em)
#text(size: 22pt, weight: "bold")[{{title}}]
#v(0.4em)
#text(size: 11pt, fill: luma(90))[Prepared by {{author}} · {{date}}]
#line(length: 100%, stroke: 0.5pt + luma(180))

#outline(indent: auto)

#pagebreak()

= Executive Summary

Provide a brief overview of scope, findings, and recommendations.

= Background

Detail context, constraints, and requirements.

= Analysis

#figure(
  table(
    columns: (1fr, auto, auto),
    [*Metric*], [*Baseline*], [*Result*],
    [Throughput], [100], [142],
    [Latency], [12 ms], [8 ms],
  ),
  caption: [Summary comparison table.],
) <tab:summary>

= Recommendations

Outline concrete next steps.
