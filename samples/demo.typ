// TypX Demo & Verification Document
// Try opening this file in TypX:
//   typx samples/demo.typ
//   typx --no-lsp samples/demo.typ
//
// Useful shortcuts to try inside TypX:
//   Alt+C       Toggle live Unicode math concealment (:conceal)
//   Alt+O       Toggle Document Outline sidebar (:outline)
//   Alt+P       Toggle live browser preview via tinymist (:preview)
//   Alt+M       Insert/toggle inline math ($...$)
//   Alt+Shift+M Insert display math block
//   Alt+I       Open LyX-style insert palette (:insert)
//   Alt+K       Show tinymist hover documentation (:hover)
//   Ctrl+Space  Trigger tinymist completion (:lsp-complete)

#set page(paper: "a4", margin: 2cm)
#set text(size: 11pt)
#set heading(numbering: "1.1")
#let index = math.op("index")
#let summary = math.op("summary")

= Introduction to TypX <sec:overview>

*TypX* is a lightweight, keyboard-first terminal editor for *Typst*, built on
`zyedidia/micro` and inspired by _LyX_'s WYSIWYM authoring philosophy ---
fast startup, live Unicode math concealment, structural navigation, and
first-class `tinymist` LSP integration... all in a single Go binary.

Key features demonstrated in this fixture:
- *Markup & Typography*: em-dash (`---`), en-dash (`--`), ellipsis (`...`),
  *bold*, _italic_, and `inline raw` spans.
- *Math Concealment*: see @sec:math for Greek letters, arrows, big operators,
  fractions, matrices, and word-boundary safety.
- *Structures & References*: see @fig:pipeline and @tbl:shortcuts in
  @sec:structures.
- *Live Diagnostics*: see @sec:diagnostics for an intentional error that
  `tinymist` highlights in the gutter and `:diagnostics`.

= Mathematical Typesetting <sec:math>

Move the cursor onto any math line to see the raw Typst source unconceal
automatically; move away to view rendered Unicode glyphs.

== Inline Math & Word-Boundary Safety <sec:inline-math>

Consider a smooth map $f: X -> Y$ with $alpha, beta in RR$ such that
$forall x in X, f(x) >= 0$ and $x^2 + y_0 <= infinity$.
Word-boundary rules keep ordinary identifiers untouched:
$sin(theta) + index + summary != 0$ (only `theta` and `!=` conceal).

== Sums, Products, Integrals & Fractions <sec:calculus>

The Gaussian density and series identities exercise Greek letters, fractions,
big operators, and arrows:

$
  Gamma(z) = integral_0^infinity t^(z - 1) e^(-t) dif t,
  quad
  frac(partial f, partial x) approx lim_(Delta x -> 0) frac(f(x + Delta x) - f(x), Delta x)
$

$
  sum_(i=1)^n a_i <= product_(k=1)^m (1 + lambda_k)
  quad <=> quad
  exists epsilon > 0, quad abs(alpha - beta) < epsilon
$

== Matrices & Piecewise Cases <sec:matrices>

An affine transformation matrix with Greek parameters and a piecewise function:

$
  Lambda = mat(
    cos(theta), -sin(theta), alpha;
    sin(theta),  cos(theta), beta;
    0,           0,          1
  ),
  quad
  sigma(x) = cases(
    x^2 & "if" x >= 0,
    -x^2 & "if" x < 0,
  )
$

Set and logic notation:
$A subset.eq B union C$, $A inter B != emptyset$, $x |-> nabla f(x) plus.minus Omega$.

= Document Structures & Outline <sec:structures>

Press `Alt+O` (`:outline`) to open the Document Outline sidebar and jump across
headings and figures.

#figure(
  rect(inset: 10pt, radius: 4pt, stroke: 0.75pt)[
    *TypX Editor* $->$ `tinymist lsp` $->$ *Diagnostics, Completion, Hover & Live Preview*
  ],
  caption: [TypX and tinymist architecture pipeline],
) <fig:pipeline>

#figure(
  table(
    columns: 3,
    align: (left, left, left),
    [*Feature*], [*Command*], [*Default Key*],
    [Live Preview], [`:preview`], [`Alt+P`],
    [Document Outline], [`:outline`], [`Alt+O`],
    [Math Concealment], [`:conceal`], [`Alt+C`],
    [Inline / Display Math], [`:math-inline` / `:math-display`], [`Alt+M` / `Alt+Shift+M`],
    [Insert Palette], [`:insert`], [`Alt+I`],
    [LSP Completion / Hover], [`:lsp-complete` / `:hover`], [`Ctrl+Space` / `Alt+K`],
  ),
  caption: [Core TypX commands and default keybindings],
) <tbl:shortcuts>

As illustrated in @fig:pipeline and summarized in @tbl:shortcuts, every command
is accessible via both `Ctrl+E` command mode and customizable keybindings.

== Raw Blocks & Nested Comments <sec:code>

The fenced block and nested block comment below intentionally contain heading
syntax so the Document Outline parser can verify that they are ignored:

```typ
= Ignored Heading Inside Fenced Code Block
#let greet(name) = [Hello, *#name*!]
```

/* Block comment with /* nested comment */ and
= Ignored Heading Inside Block Comment
*/

= Diagnostics Verification <sec:diagnostics>

When `tinymist` is running, uncomment the line below to trigger a live
diagnostic in the editor gutter and `:diagnostics` output (leave it commented
out for a clean compile and live preview):

// #typx_intentional_diagnostic_demo
