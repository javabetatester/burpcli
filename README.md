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
- `enter` expande/colapsa grupo do domínio no histórico
- `x` exporta request/response para `./exports`
- `q` sai

## HTTPS (certificado / confiança)

Pra ver e editar tráfego HTTPS como Burp/Charles, o proxy precisa fazer MITM: ele se apresenta pro navegador com um certificado “do site”, mas assinado por um **CA local** seu. Aí você instala esse CA no sistema/navegador como confiável.

### 1) Exportar e instalar o CA

Gera (se não existir) e exporta o CA raiz:

```bash
go run ./cmd/burpui --ca-dir ./ca --export-ca burpui-ca.pem
```

Auto-instalar no Windows (CurrentUser):

```bash
go run ./cmd/burpui --ca-dir ./ca --install-ca
```

Instalação (resumo):

- Windows: importar `burpui-ca.pem` em “Trusted Root Certification Authorities”
- macOS: Keychain Access → System → Certificates → importar → marcar como “Always Trust”
- Linux: colocar em `/usr/local/share/ca-certificates/` e rodar `update-ca-certificates`
- Firefox: Settings → Certificates → Authorities → Import

### 2) Rodar com MITM

```bash
go run ./cmd/burpui --listen :8080 --mitm --ca-dir ./ca
```

## Limitações do MVP

## Limitações do MVP

- Por padrão, HTTPS via CONNECT faz túnel (não faz MITM/decodificação)
- Com `--mitm`, HTTPS faz MITM e exige instalar o CA
- Bodies são capturados até `--max-body` bytes
- Edit/Breakpoints só param/permitem editar quando a request tem `Content-Length` conhecido e `<= --max-body`

