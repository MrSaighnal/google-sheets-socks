<p align="center">
  <img alt="GSS" src="https://raw.githubusercontent.com/MrSaighnal/google-sheets-socks/refs/heads/main/images/gssocks.png?token=GHSAT0AAAAAAC26QHV6NCSDRNWLITJGUW4E2EQ67EA" height="200" /><br />
<a href="https://twitter.com/mrsaighnal"><img src="https://img.shields.io/twitter/follow/mrsaighnal?style=social" alt="twitter" style="text-align:center;display:block;"></a>
</p>

# GSS - Google Sheets SOCKS

**Google Sheets SOCKS (GSS)** is a Proof-of-Concept (PoC) tool that implements a full **Command & Control (C2)** communication channel using **Google Sheets** as the medium.  
It enables **SOCKS5-like proxying over spreadsheets**, making it a powerful alternative when it’s too risky or impractical to deploy a classic C2 infrastructure.

> ✅ No need for VPS, DNS tunneling, or custom domains – just Google Sheets and a service account.

---

## 🧠 Concept

GSS creates a covert bidirectional communication channel between a **client** (typically running on a target system) and a **server** (controlled by the operator).  
All traffic is tunneled through Google Sheets in base64-encoded chunks, simulating SOCKS5 behavior internally.

- The client writes data to the sheet using a connection ID and a role tag (`client`).
- The server reads and processes this data, opens the target connection, and sends the response back via the same sheet (`server` role).
- This allows arbitrary TCP connections from inside hardened environments with only Google services whitelisted.

---

## ⚙️ How It Works

1. **Client** opens a local TCP listener (default: `:9191`) and encodes all data in base64.
2. **Each chunk** is written to Google Sheets using a unique `connID`.
3. **Server** polls Google Sheets:
   - Waits for SOCKS5 handshake data
   - Parses the target address from the payload
   - Connects to the real destination
4. **Server response** is encoded, uploaded, and later read by the client.
5. Communication continues asynchronously, chunk by chunk.

---

## 📷 Screenshots

_Coming soon..._

---

## 📋 Requirements

- A Google Cloud project with **Sheets API enabled**
- At least one **Service Account** with access to the target spreadsheet
- A Google Sheet named `"Foglio1"` (default) with columns A–H
- Basic Go environment (`go 1.18+`)

---

## 🚀 Usage

1. Create a Google Sheet and share it with the Service Account email.
2. Place the service account JSON inside the `rawCredentials` array.
3. Build the tools:

```bash
go build -o client ./client.go
go build -o server ./server.go
