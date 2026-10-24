import json,sys,runpy,os
f=json.load(open('/root/w13-private-fixture.json'))
sys.argv=['mail-client-smoke.py','--ca-file','/var/lib/urmail-agent/certs/mail/box.w13.test.cert','--','box.w13.test',f['address'],f['password']]
runpy.run_path(os.environ.get('URMAIL_W13_SOURCE','/workspace')+'/scripts/mail-client-smoke.py',run_name='__main__')
