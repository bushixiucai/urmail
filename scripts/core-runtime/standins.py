#!/usr/bin/env python3
"""Explicit loopback model and dedicated TLS SMTP stand-ins; no external egress."""
import http.server,json,socketserver,ssl,threading,time
from pathlib import Path
v=dict(x.split('=',1) for x in Path('/etc/urmail/urmaild.env').read_text().splitlines() if '=' in x)
host=v['URMAIL_MYHOSTNAME'];lock=threading.Lock();facts={'ai':0,'marketing':0,'rejected':0,'marketing_ids':[]}
def count(kind,mid=None):
 with lock:
  facts[kind]+=1
  if mid:facts['marketing_ids'].append(mid)
  Path('/root/core-standins.json').write_text(json.dumps(facts))
class Model(http.server.BaseHTTPRequestHandler):
 def do_POST(self):
  assert self.path=='/api/chat'
  data=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  time.sleep(.1);count('ai')
  result={'model':data['model'],'message':{'role':'assistant','content':json.dumps({'summary':'Deterministic bounded loopback summary'})},'done':True,'prompt_eval_count':10,'eval_count':5}
  raw=json.dumps(result).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
 def log_message(self,*args):pass
class SMTP(socketserver.StreamRequestHandler):
 def handle(self):
  self.wfile.write(b'220 dedicated loopback fixture\r\n')
  while True:
   line=self.rfile.readline(65537)
   if not line:return
   verb=line.split(b' ',1)[0].strip().upper()
   if verb in (b'EHLO',b'HELO'):self.wfile.write(b'250-local fixture\r\n250 SIZE 1000000\r\n')
   elif verb==b'RCPT':
    if b'@core.perf.test>' not in line.lower():count('rejected');self.wfile.write(b'550 fixture domain only\r\n')
    else:self.wfile.write(b'250 recipient accepted\r\n')
   elif verb in (b'MAIL',b'RSET',b'NOOP'):self.wfile.write(b'250 OK\r\n')
   elif verb==b'DATA':
    self.wfile.write(b'354 end with dot\r\n');body=[]
    while True:
     part=self.rfile.readline(1000001)
     if part in (b'.\r\n',b''):break
     body.append(part)
    raw=b''.join(body);mid=next((x.decode().strip() for x in raw.splitlines() if x.lower().startswith(b'message-id:')),None)
    time.sleep(.15);count('marketing',mid);self.wfile.write(b'250 accepted by isolated sink\r\n')
   elif verb==b'QUIT':self.wfile.write(b'221 bye\r\n');return
   else:self.wfile.write(b'502 unsupported\r\n')
class TLS(socketserver.ThreadingTCPServer):
 allow_reuse_address=True;daemon_threads=True
 def get_request(self):
  sock,addr=super().get_request();return tls.wrap_socket(sock,server_side=True),addr
root=Path('/var/lib/urmail-agent/certs/mail');tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);tls.load_cert_chain(root/(host+'.cert'),root/(host+'.key'))
server=TLS(('127.0.0.1',1465),SMTP);threading.Thread(target=server.serve_forever,daemon=True).start()
http.server.ThreadingHTTPServer(('127.0.0.1',11434),Model).serve_forever()
