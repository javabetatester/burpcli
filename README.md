## Rodar

```bash
go run ./cmd/burpui --listen :8080
```

Configure seu navegador/aplicação para usar o proxy em `127.0.0.1:8080`.

Atalhos:

- `i` liga/desliga intercept
- `b` abre breakpoints (a adicionar, enter alterna, del remove)
- `f` forward (quando pendente)
- `d` drop (quando pendente)
- `e` edit (quando pendente, Ctrl+S aplica/forward)
- `r` repeater (Ctrl+S envia, Esc volta)
- `c` compose (nova requisição, Ctrl+S envia, Esc volta)
- `x` exporta request/response para `./exports`
- `q` sai

## Limitações do MVP

- HTTPS via CONNECT faz túnel (não faz MITM/decodificação)
- Bodies são capturados até `--max-body` bytes
- Edit/Breakpoints só param/permitem editar quando a request tem `Content-Length` conhecido e `<= --max-body`

