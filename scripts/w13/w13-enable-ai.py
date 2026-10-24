import os
import json,subprocess
from importlib.machinery import SourceFileLoader
Panel=SourceFileLoader('panel',os.environ.get('URMAIL_W13_SOURCE','/workspace')+'/scripts/remediation-http-smoke.py').load_module().Panel
subprocess.run(['runuser','-u','postgres','--','psql','-d','urmail','-v','ON_ERROR_STOP=1','-c',"SET ROLE urmail_platform; UPDATE plans SET features=features||'{\"ai\":{\"calls\":20,\"mailbox_calls\":10,\"concurrency\":1}}'::jsonb WHERE code='free'; UPDATE subscriptions SET plan_snapshot=jsonb_set(plan_snapshot,'{features}',(SELECT features FROM plans WHERE code='free'),true) WHERE tenant_id=1 AND plan_id='free'"],check=True,stdout=subprocess.DEVNULL)
creds=dict(line.strip().split('=',1)for line in open('/root/urmail-initial-admin.txt')if '='in line);p=Panel('http://127.0.0.1:8080');p.login(creds['email'],creds['password']);
try:p.call('PUT','/api/v1/ai/configuration',{'mode':'local','provider':'ollama','endpoint':'http://127.0.0.1:11434','model':'w13-local','enabled':True,'local_only':True},expected=204)
except json.JSONDecodeError:pass
assert p.call('GET','/api/v1/ai/configuration')['configuration']['enabled']
print('Enabled local-only fixture model with explicit tenant and mailbox call caps; no platform model cost or paid endpoint.')
