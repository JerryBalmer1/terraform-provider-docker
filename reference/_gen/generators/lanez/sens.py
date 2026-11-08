import sys; sys.path.insert(0, sys.argv[1])
from frag import load, save
D={L:load(L) for L in 'ABCDE'}
W='WHOLE'
JT="Every field is a secret: worker and manager join tokens each let their holder join the swarm."
CMP=['services.env_file','services.environment','services.secrets','services.build.dockerfile_inline','services.build.args',
     'services.build.ssh','services.build.secrets']
JOBS=[p.replace('services.','jobs.',1) for p in CMP]
TOP=['include.env_file','secrets.environment','configs.content','configs.environment','volumes.driver_opts']
SVC=lambda pre:[pre+'container_spec.env',pre+'container_spec.mounts.volume_options.driver_config.options']
RULES={
 ('A','engine:container','inspect'):['config.env'], ('A','engine:container-data','inspect'):['config.env'],
 ('A','engine:plugin','inspect'):['settings.env','config.env'], ('A','engine:plugin-data','inspect'):['settings.env','config.env'],
 ('A','engine:plugins','plugins'):['settings.env','config.env'],
 ('A','engine:volume','inspect'):['options'], ('A','engine:volume-data','inspect'):['options'], ('A','engine:volumes','volumes'):['volumes.options'],
 ('A','engine:image-attestations','attestations'):['statement','descriptor.data'],
 ('A','engine:image-history','history'):['created_by'],
 ('A','engine:registry-auth','auth'):['identity_token'],
 ('B','swarm:service','inspect'):SVC('spec.task_template.'), ('B','swarm:service-data','inspect'):SVC('spec.task_template.'),
 ('B','swarm:services','services'):SVC('spec.task_template.'),
 ('B','swarm:task-data','inspect'):SVC('spec.'), ('B','swarm:tasks','tasks'):SVC('spec.'),
 ('B','swarm:swarm-init','join_tokens'):(W,JT), ('B','swarm:swarm-data','join_tokens'):(W,JT),
 ('B','swarm:swarm-rotate-tokens','join_tokens'):(W,JT),
 ('C','compose:name','project'):CMP+JOBS+TOP, ('C','compose:cli-config','project'):CMP+JOBS+TOP,
 ('C','compose:services','project'):CMP, ('C','compose:jobs','project'):JOBS,
 ('C','compose:configs','project'):['configs.content','configs.environment'],
 ('C','compose:secrets','project'):['secrets.environment'], ('C','compose:include','project'):['include.env_file'],
 ('C','compose:volumes','project'):['volumes.driver_opts'], ('C','compose:networks','project'):[], ('C','compose:models','project'):[],
 ('C','compose:service-build','build'):['dockerfile_inline','args','ssh','secrets'],
 ('C','compose:cli-ps','containers'):[], ('C','compose:cli-top','processes'):[],
 ('C','compose:cli-logs','entries'):['line'],
 ('C','compose:cli-exec','result'):['stdout','stderr'], ('C','compose:cli-run','result'):['stdout','stderr'],
 ('D','engine:image','stages'):['instructions.env','instructions.arg','instructions.created_by'],
 ('D','dockerfile:data-dockerfile','stages'):['instructions.env','instructions.arg','instructions.created_by'],
 ('D','engine:image','inspect'):['config.env'], ('D','engine:image-data','inspect'):['config.env'],
 ('D','engine:image-data','config'):['env'], ('D','engine:registry-image-data','config'):['env'],
 ('D','dockerfile:data-dockerfile','global_args'):['default'], ('D','dockerfile:data-dockerfile','history'):['created_by'],
 ('D','dockerfile:ARG','arg'):['args.default'], ('D','dockerfile:ENV','env'):['env'],
 ('E','buildx:builder','builder'):['nodes.driver_opts'], ('E','buildx:builder-inspect','builder'):['nodes.driver_opts'],
 ('E','buildx:builders','builders'):['nodes.driver_opts'],
 ('E','buildx:build','build_metadata'):['buildx_build_provenance'], ('E','buildx:bake','metadata'):['buildx_build_provenance'],
 ('E','buildx:history-inspect','record'):['context','vcs_repository','config','materials.uri'],
 ('E','buildx:imagetools-inspect','image_config'):['config','history.created_by'],
 ('E','cli-only:manifest-inspect','image_config'):['config','history.created_by'],
 ('E','buildx:bake','bake'):['target.args','target.dockerfile_inline','target.secret','target.ssh'],
 ('E','cli-only:stack','stack'):['services.environment','services.env_file','services.secrets'],
 ('E','cli-only:stack','service_status'):['spec'], ('E','cli-only:stack-services','services'):['spec'],
}
def paths(fs,p=''):
    for f in fs:
        q=p+f['name']; yield q,f; yield from paths(f.get('fields') or [],q+'.')
miss=[];done=0
for (L,iid,name),rule in RULES.items():
    o=[o for o in D[L]['items'][iid]['outputs'] if o['name']==name]; assert len(o)==1,(iid,name); o=o[0]
    assert o['sensitive'] is True and o['category'] in('structured','result'),(iid,name)
    allp=dict(paths(o.get('fields') or []))
    for q,f in allp.items(): f.pop('sensitive',None)
    if isinstance(rule, tuple):
        o['sensitive_reason']=rule[1]; done+=1; continue
    o['sensitive']=False
    for q in rule:
        if q in allp: allp[q]['sensitive']=True
        else: miss.append((L,iid,name,q))
    done+=1
# all V5 outputs covered?
for L,d in D.items():
    for iid,it in d['items'].items():
        for o in it['outputs']:
            if o['category'] in('structured','result') and o['sensitive'] and not o.get('sensitive_reason'): print('UNCOVERED',L,iid,o['name'])
for L,d in D.items(): save(L,d)
print('rules applied',done); [print('MISSING',*m) for m in miss]
