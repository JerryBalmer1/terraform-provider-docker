import sys; sys.path.insert(0, sys.argv[1])
from frag import load, save, out
D={L:load(L) for L in 'ABCDE'}
log=[]
def recat(L,iid,name,cat):
    o=out(D[L],iid,name); log.append((L,iid,name,o['category'],cat)); o['category']=cat
def insert_after(L,iid,after,new):
    outs=D[L]['items'][iid]['outputs']; i=[o['name'] for o in outs].index(after); outs.insert(i+1,new)

# ---------- 1. logs: both `logs` (text) and `logs_json` (entries) ----------
ENTRY_HOW=("JSON array with one object per log line, in arrival order: {\"timestamp\": RFC 3339 nano from the frame "
           "(timestamps=true), \"stream\": \"stdout\"|\"stderr\" from the multiplexed frame header (\"stdout\" for a TTY), \"line\": the text without the trailing newline}")
def hash_out(raw, desc_extra=""):
    return {"name": raw+"_sha256","tf_type":"string","category":"hash",
            "source":{"type":"derived","from":raw,"how":"hex sha256 of the UTF-8 bytes of "+raw},
            "known_after_apply":False,"sensitive":False,"hash_of":raw,"description":"sha256 of "+raw+".","provided":False}
for L,iid,kind in (('A','engine:container-logs','container'),('B','swarm:service-logs','service'),('B','swarm:task-logs','task')):
    items=D[L]['items'][iid]; outs=items['outputs']
    old='logs' if L=='A' else 'logs_text'
    o=out(D[L],iid,old)
    if old!='logs':
        o['name']='logs'; o['description']=o['description'].replace('logs_text','logs')
    h=out(D[L],iid,'content_sha256'); outs.remove(h)
    lj={"name":"logs_json","tf_type":"string","category":"raw",
        "source":{"type":"derived","from":"the "+kind+" log stream ("+o['source']['pointer'].split('/')[-4].replace('~1','/') if False else "the same stream as logs","how":ENTRY_HOW},
        "known_after_apply":o['known_after_apply'],"sensitive":True,
        "description":"The same log lines as a JSON array of {timestamp, stream, line} entries. Sensitive: logs routinely contain secrets.","provided":False}
    i=outs.index(o); outs.insert(i+1,lj)
    outs.insert(i+2,hash_out('logs')); outs.insert(i+3,hash_out('logs_json'))
    for k in ('outputs_note',):
        if k in items: items[k]=items[k].replace('content_sha256','logs_sha256, logs_json_sha256').replace('logs_text','logs, logs_json')
    log.append((L,iid,'logs','+logs_json, content_sha256 -> logs_sha256 + logs_json_sha256'))
# compose logs: has logs_json (serializes entries); add text logs
iid='compose:cli-logs'; outs=D['C']['items'][iid]['outputs']
h=out(D['C'],iid,'content_sha256'); outs.remove(h)
lj=out(D['C'],iid,'logs_json'); i=outs.index(lj)
outs.insert(i,{"name":"logs","tf_type":"string","category":"raw",
    "source":{"type":"derived","from":"entries","how":"one line per entry, as `docker compose logs` prints it without color: \"<container_name>  | <line>\", joined with \n"},
    "known_after_apply":True,"sensitive":True,
    "description":"The log lines as plain text, prefixed with the container name like `docker compose logs`. Sensitive: application output can print secrets."})
outs.insert(i+2,{k:v for k,v in hash_out('logs').items() if k!='provided'} | {"known_after_apply":False})
outs.insert(i+3,{k:v for k,v in hash_out('logs_json').items() if k!='provided'})
log.append(('C',iid,'logs','+logs, content_sha256 -> logs_sha256 + logs_json_sha256'))

# ---------- 2. result category ----------
for L,d in D.items():
    pass
import json
inv=json.load(open(sys.argv[2],encoding='utf-8'))
kind={it['id']:it['kind'] for it in inv['items']}
for L,d in D.items():
    for iid,it in d['items'].items():
        if kind[iid]=='action':
            for o in it['outputs']:
                if o['category']=='structured': recat(L,iid,o['name'],'result')
for L,iid,name in (('B','swarm:swarm-ca-rotate','root_rotation_in_progress'),('B','swarm:service-force-update','force_update'),
                   ('D','engine:image-data','created'),('D','engine:registry-image-data','created'),
                   ('D','dockerfile:data-dockerfile','reconstructed'),('D','dockerfile:data-dockerfile','fidelity'),
                   ('E','cli-only:stack','service_status')):
    recat(L,iid,name,'result')

# ---------- 3. version names ----------
for iid in ('buildx:builder','buildx:builder-inspect'):
    recat('E',iid,'buildkit_version','version')
for iid,it in D['E']['items'].items():
    if kind[iid] in ('resource','data_source','action') and iid.startswith('cli-only:'):
        outs=it['outputs']; vs=[i for i,o in enumerate(outs) if o['category']=='version']
        outs.insert(vs[-1]+1,{"name":"docker_cli_version","tf_type":"string","category":"version",
            "source":{"type":"sources_md","pin":"docker_cli"},"known_after_apply":False,"sensitive":False,
            "description":"docker CLI release whose client-side behaviour this type reproduces (the docker_cli pin in SOURCES.md, v29.5.3)."})
        log.append(('E',iid,'docker_cli_version','added'))
for L,d in D.items(): save(L,d)
for l in log: print(*l)
