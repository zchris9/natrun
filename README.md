# Natrun

A concept + demo of an execution environment for programs written in natural language on language-model (LM) fabric, leaning on the **LLM OS** principle: https://youtu.be/zjkBMFhNj_g?t=2535.

Disclaimer: This README.md was written with the help of an LLM. Code in this repository was written by an LLM, with some manual edits. Review the code before running it in any environment you care about. The examples are direct LLM output.

---

## Concept

Natrun treats the LM as a general programming substrate: a program is a hidden system prompt, and execution is an iterated LM trajectory under that prompt. On each input event, the LM, conditioned on the system prompt and the history of the run so far, emits the next output artifact. System state is carried in a context object, where input is injected at any time, and output is extracted on every LM termination. A user interface is one specialization of this scheme — the case where outputs are renderables and inputs are user actions — but nothing in the model is specific to UI.

### Symbols

| Symbol | Meaning |
|---|---|
| $S$ | system prompt (the program; fixed, hidden, server-side) |
| $c_n$ | carried context at turn $n$ (program state) |
| $i_n$ | system boundary input at turn $n$ |
| $o_n$ | system boundary output at turn $n$ |
| $M$ | language model: text $\mapsto$ text |
| $P$ | parser: text $\mapsto$ output artifact |

### Iteration

The program advances on every iteration, which is triggered immediately when an input $i_n$ is available, according to the following rules, with $\Vert$ being the concatenation operator.

$$c_n \;=\; (c_{n-1} \,\Vert\, i_{n}) \,\Vert\, M(S \,\Vert\, c_{n-1} \,\Vert\, i_n)$$

$$o_n \;=\; P(c_n)$$

$o_n$ is then handed to whatever consumes the output.

### Initial state

The initial carried context $c_0$ and the input event $i_0$ are empty. The first turn is initiated by the system. At $n = 1$ the iteration rule has the form

$$c_1 \;=\; i_1 \,\Vert\, M(S \,\Vert\, i_1)$$

$$o_1 \;=\; P(c_1)$$

---

## This repository

A reference implementation of the concept, in Go, for the UI specialization: outputs are HTML and inputs are user actions on the rendered HTML.

- $S$ is a string loaded from a prompt file. It also defines the vocabulary of $i_n$: the model both emits the interactive elements that carry actions and interprets those actions on the next turn, so the action schema is the prompt's responsibility, not the runtime's.
- $c_n$ is a JSON object containing the full transcript so far — an array of {action, reply} pairs, where each reply is the raw text the model emitted that turn. It is carried by the client, making the server stateless.
- $i_n$ is a small JSON object — typically `{"type": "..."}` with an optional payload — declared on an HTML element via `data-natrun-action`. The loader posts it to the server whenever the user clicks or submits that element. Forms additionally include their field values under `"form"`.
- $o_n$ is a HTML fragment parsed from the last LM output
- $M$ is implemented by two Anthropic models, one selected for turn 1, and the other one for consecutive turns.
- $P$ generates $o_n$ from the latest language model reply

### Signing

A practical way to prevent the client from tampering with the carried context is described below.

The problem is that a malicious or curious client can mutate $c_{n-1}$ before sending it back to trick the model into prohibited output.

Signing closes this gap without giving up statelessness on the server. The server keeps a secret $\sigma$ and stamps every $c_n$ it emits with a keyed message-authentication code

$$s_n \;=\; H_\sigma(c_n).$$

The client transmits $c_n$ and $s_n$ side by side on the next request. The signature is metadata for the server, not input to the model: on the next turn the server verifies $H_\sigma(c_{n-1}) = s_{n-1}$ before invoking $M$, and if the check fails the turn is rejected. The client may read $c_n$ but cannot produce a valid $s_n$ for a $c_n$ it forged, because $\sigma$ never leaves the server.


### Structure

- [`cmd/natrun/`](cmd/natrun/) — single-binary HTTP server
- [`internal/signing/`](internal/signing/) — HMAC-SHA256 sign and verify
- [`internal/model/`](internal/model/) — Anthropic Messages API client
- [`internal/promptio/`](internal/promptio/) — fenced-block parser
- [`examples/`](examples/) — example programs; one file = one site
- [`web/`](web/) — the loader shell

### Run it

Go must be available on the host, and the environment must be configured. Copy `.env.example`, name it `.env` and fill it with:

- `ANTHROPIC_API_KEY`: API key from Anthropic
- `NATRUN_SECRET`: Can be chosen randomly
- `NATRUN_SITE`: The source file in the examples folder which is used
- `NATRUN_MODEL_FIRST`: The LM used for the initial output
- `NATRUN_MAX_TOK_FIRST`: Maximum number of output tokens for the first output
- `NATRUN_MODEL_NEXT`: The LM used for every consecutive output
- `NATRUN_MAX_TOK_NEXT`: Maximum number of output tokens for every consecutive output
- `NATRUN_ADDR`: The listen address. Default is `:8000`.

Then run natrun:

```console
go run ./cmd/natrun
```
