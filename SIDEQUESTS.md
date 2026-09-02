# Sidequests

Things skipped over during handholding to keep momentum, meant to be
learned properly later rather than never. Not a design record — see
`DESIGN.md` for that. Check items off as you go learn them; leave the
line even once checked so this stays a record of what got skipped and
when.

## Stage 1 — Bootstrap

- [ ] `http.ServeMux` vs. a third-party router (chi, gorilla/mux) —
      what a router buys you (path params, middleware chaining) that
      the stdlib mux doesn't, and when it's worth the dependency.
- [ ] `flag` package basics — how it parses, `flag.Parse()` timing,
      why flags are pointers.
- [ ] Graceful shutdown — `signal.Notify` + `http.Server.Shutdown`
      instead of a bare `ListenAndServe`. Not needed for a healthz
      stub, but every real server wants it.
