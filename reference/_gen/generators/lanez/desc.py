import sys; sys.path.insert(0, sys.argv[1])
from frag import load, save, out
D={L:load(L) for L in 'BC'}
n=0
for L,d in D.items():
    for iid,it in d['items'].items():
        for o in it['outputs']:
            s=o.get('description','')
            new=s.replace(" Sensitive: ContainerSpec.Env (and Args/Command) can carry secret values.",
                          " Only ContainerSpec.Env and volume driver options are sensitive (field-level); the rest, including Args/Command, stays plain in plans.")
            if not o['sensitive']:
                for a,b in ((" Sensitive: inline `content` and environment-sourced values."," Field-level sensitive: inline `content` and environment-sourced values."),
                            (" Sensitive: file/environment sources resolve to secret values."," Field-level sensitive: environment-sourced values."),
                            (" Sensitive: args, secrets, ssh."," Field-level sensitive: args, secrets, ssh, dockerfile_inline."),
                            (" Sensitive: output can print secrets."," Field-level sensitive: stdout and stderr (output can print secrets); exit_code stays plain.")):
                    new=new.replace(a,b)
            if new!=s: o['description']=new; n+=1
        on=it.get('outputs_note','')
        nn=on.replace("inspect/inspect_json are sensitive (Env)","inspect_json is sensitive; inspect is sensitive only at ContainerSpec.Env (field-level)") \
             .replace("services/services_json sensitive (Env)","services_json sensitive; services only at ContainerSpec.Env (field-level)") \
             .replace("tasks/tasks_json sensitive (Env)","tasks_json sensitive; tasks only at ContainerSpec.Env (field-level)")
        if nn!=on: it['outputs_note']=nn; n+=1
for L,d in D.items(): save(L,d)
print('updated',n)
