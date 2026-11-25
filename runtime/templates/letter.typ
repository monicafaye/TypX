// @title: Formal Letter
// @desc: Clean letter layout with sender, recipient, subject, and signature block
// @category: Correspondence

#set page(paper: "a4", margin: (x: 2.5cm, y: 2.5cm))
#set text(size: 11pt)
#set par(justify: false, leading: 0.65em)

#align(right)[
  *{{author}}* \
  123 Example Street \
  City, Postal Code \
  email\@example.com \
  #v(0.5em)
  {{date}}
]

#v(1em)

Recipient Name \
Organization \
456 Destination Ave \
City, Postal Code

#v(1.2em)

*Subject: {{title}}*

#v(0.6em)

Dear Recipient,

Write the opening paragraph stating your purpose clearly and concisely.

Add supporting details or context in the body paragraphs.

#v(1em)

Sincerely,

#v(1.5em)

{{author}}
