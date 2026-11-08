import json
IND={'A':1,'B':1}
def load(L):
    return json.load(open(L+'.json',encoding='utf-8'))
def save(L,d):
    s=json.dumps(d,indent=IND.get(L,2),ensure_ascii=False)+'\n'
    open(L+'.json','w',encoding='utf-8',newline='\n').write(s)
def out(d,iid,name):
    m=[o for o in d['items'][iid]['outputs'] if o['name']==name]
    assert len(m)==1,(iid,name)
    return m[0]
