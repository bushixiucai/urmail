import http.server,json,pathlib,hashlib
record=pathlib.Path('/root/w13-model-observations.json')
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_GET(self):
  data={'models':[{'name':'w13-local'}]};raw=json.dumps(data).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(raw)
 def do_POST(self):
  assert self.path=='/api/chat';raw=self.rfile.read(int(self.headers['Content-Length']));body=json.loads(raw);messages=body.get('messages',[]);record.write_text(json.dumps({'calls':json.loads(record.read_text()).get('calls',0)+1 if record.exists()else 1,'payload_sha256':hashlib.sha256(raw).hexdigest(),'has_mail_body':any('Protocol acceptance' in m.get('content','')for m in messages),'path':self.path}));response={'model':'w13-local','message':{'role':'assistant','content':json.dumps({'summary':'Local fixture summary for the selected authenticated mailbox.'})},'done':True,'prompt_eval_count':10,'eval_count':5};encoded=json.dumps(response).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(encoded)
http.server.HTTPServer(('127.0.0.1',11434),Handler).serve_forever()
