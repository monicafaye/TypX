#set page(paper: "a4", margin: (x: 2.2cm, y: 2cm))
#set text(size: 10.5pt)
#set heading(numbering: "1.1")
#set par(justify: true)

#align(center)[
  #text(size: 18pt, weight: "bold")[Spectral Decay & Integral Transforms]
  #v(2pt)
  #text(size: 10pt, fill: luma(90))[TypX Authoring Showcase --- Live Math, Outline & Preview]
]

#v(6pt)

= Overview <sec:overview>

For a smooth, rapidly decaying function $f: RR -> CC$, the *Fourier transform*
links spatial smoothness to spectral decay. Let $alpha, beta in RR$ with
$forall x in RR, abs(f(x)) <= C (1 + x^2)^(-1)$ and $lambda_0 > 0$.

#block(
  width: 100%,
  inset: 10pt,
  radius: 4pt,
  fill: rgb("#f0f7fa"),
  stroke: (left: 3pt + rgb("#239dad")),
)[
  *Theorem (Parseval Identity).* For every $f, g in L^2(RR)$,
  $
    integral_(-infinity)^infinity f(x) overline(g(x)) dif x
    = frac(1, 2 pi) integral_(-infinity)^infinity hat(f)(omega) overline(hat(g)(omega)) dif omega
  $
]

= Analytic Estimates <sec:estimates>

== Gamma & Series Bounds <sec:gamma>

The Euler integral and discrete product bounds satisfy:

$
  Gamma(z) = integral_0^infinity t^(z - 1) e^(-t) dif t,
  quad
  sum_(n=1)^infinity a_n <= product_(k=1)^m (1 + lambda_k)
$

$
  forall epsilon > 0, exists N in NN : quad
  n >= N => abs(S_n - L) < epsilon
  quad <=> quad
  lim_(n -> infinity) S_n = L
$

== Differential Operators <sec:diff>

On a compact domain $Omega subset.eq RR^n$ with boundary $partial Omega$, the
Laplacian obeys $Delta u = nabla dot (nabla u) = 0$ and preserves symmetry:
$sin(theta) + cos(theta) != 0$ whenever $theta != -pi / 4$.

= Summary of Operators <sec:summary>

#figure(
  table(
    columns: (auto, 1fr, auto),
    inset: 7pt,
    stroke: 0.5pt + luma(180),
    fill: (_, row) => if row == 0 { rgb("#eef6f8") } else { none },
    [*Operator*], [*Definition*], [*Domain*],
    [Fourier $hat(f)$], [$integral_(-infinity)^infinity f(x) e^(-i omega x) dif x$], [$L^1(RR) inter L^2(RR)$],
    [Gamma $Gamma(z)$], [$integral_0^infinity t^(z-1) e^(-t) dif t$], [$Re(z) > 0$],
    [Gradient $nabla f$], [$(frac(partial f, partial x_1), dots, frac(partial f, partial x_n))$], [$C^1(Omega)$],
  ),
  caption: [Core integral and differential operators],
) <tbl:operators>

As summarized in @tbl:operators and @sec:estimates, all identities above compile
instantly with `tinymist` while remaining clean and readable in TypX.
