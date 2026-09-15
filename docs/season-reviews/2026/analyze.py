import json, glob, os, collections, statistics, re, sys
S=os.environ["S"]
out={}
# ---------- 1. Recap weeks: efficiency + records ----------
weeks=[]
for f in sorted(glob.glob(f"{S}/recap/week-*.json")):
    d=json.load(open(f)); weeks.append(d)
weeks.sort(key=lambda d:d["week_number"])
eff=collections.defaultdict(lambda:{"act":0.0,"opt":0.0,"weeks":0,"w":0,"l":0,"t":0,"name":""})
me="a2u7l4hwmj203yvz"
mine_weeks=[]
for d in weeks:
    for t in d["teams"]:
        e=eff[t["team_id"]]; e["act"]+=t["actual_pts"]; e["opt"]+=t["optimal_pts"]; e["weeks"]+=1; e["name"]=t["team_name"]
        if t["team_id"]==me: mine_weeks.append((d["week_number"], t["actual_pts"], t["optimal_pts"], round(t["efficiency"],3)))
    for m in d["matchups"]:
        if m.get("is_tie"):
            eff[m["home_team_id"]]["t"]+=1; eff[m["away_team_id"]]["t"]+=1
        else:
            eff[m["winner_id"]]["w"]+=1; eff[m["loser_id"]]["l"]+=1
table=sorted(((v["name"], v["act"]/v["opt"] if v["opt"] else 0, v["act"], v["opt"], v["w"], v["l"], v["weeks"], k==me) for k,v in eff.items()), key=lambda x:-x[1])
out["efficiency"]=[{"team":n,"eff":round(e,4),"actual":a,"optimal":o,"W":w,"L":l,"weeks":wk,"me":m} for n,e,a,o,w,l,wk,m in table]
out["my_weeks"]=mine_weeks
out["weeks_rendered"]=[d["week_number"] for d in weeks]
# ---------- 2. Ledger ----------
recs=[]
for f in glob.glob(f"{S}/s3/runledger/**/*.json", recursive=True):
    try: recs.append(json.load(open(f)))
    except: pass
bycmd=collections.defaultdict(lambda:collections.Counter())
bymonth=collections.Counter(); failmonth=collections.Counter()
first=min(r.get("started_at","9") for r in recs); last=max(r.get("started_at","0") for r in recs)
for r in recs:
    c=r.get("command","?").split(" ")[0]; st=r.get("status","?")
    bycmd[c][st]+=1; m=r.get("started_at","")[:7]; bymonth[m]+=1
    if st=="FAILED": failmonth[m]+=1
out["ledger"]={"n":len(recs),"first":first,"last":last,"by_month":dict(sorted(bymonth.items())),"failed_by_month":dict(sorted(failmonth.items())),
  "by_command":{c:dict(v) for c,v in sorted(bycmd.items(), key=lambda x:-sum(x[1].values()))}}
# ---------- 3. opsalert markers ----------
oa=collections.Counter()
for f in glob.glob(f"{S}/s3/opsalert/**/*", recursive=True):
    if os.path.isfile(f):
        rel=os.path.relpath(f, f"{S}/s3/opsalert"); oa[rel.split("/")[0]]+=1
out["opsalert_markers"]=dict(oa)
# ---------- 4. Notifications ----------
notes=[]
for f in glob.glob(f"{S}/s3/notifications/**/*.json", recursive=True):
    try: notes.append(json.load(open(f)))
    except: pass
kinds=collections.Counter(n.get("kind","?") for n in notes)
lineup=[n for n in notes if n.get("kind")=="lineup"]
lineup.sort(key=lambda n:n.get("created_at",""))
perday=collections.Counter(n.get("created_at","")[:10] for n in lineup)
out["notifications"]={"n":len(notes),"kinds":dict(kinds),"lineup_days":len(perday),"lineup_per_day_mean":round(sum(perday.values())/max(1,len(perday)),2),
  "lineup_samples":[{"t":n["created_at"],"title":n["title"],"msg":n["message"][:220]} for n in lineup[-3:]]}
# re-flip: player names moved back and forth within the same calendar day
flip_days=0; flips=0; moves=0
byday=collections.defaultdict(list)
for n in lineup: byday[n.get("created_at","")[:10]].append(n)
for day,ns in byday.items():
    seen=collections.Counter()
    for n in ns:
        names=re.findall(r"^\s*[•\-\*]?\s*([A-Z][A-Za-z.'\- ]+?)\s*(?:→|->|:)", n.get("message",""), re.M)
        for nm in names: seen[nm]+=1; moves+=1
    f=sum(c-1 for c in seen.values() if c>1)
    flips+=f
    if f: flip_days+=1
out["reflips"]={"moves_parsed":moves,"repeat_moves_same_day":flips,"days_with_repeats":flip_days,"days":len(byday)}
# ---------- 5. Grades: rank skill per system ----------
def spearman(xs,ys):
    n=len(xs)
    if n<3: return None
    def ranks(v):
        idx=sorted(range(n), key=lambda i:v[i]); r=[0]*n; i=0
        while i<n:
            j=i
            while j+1<n and v[idx[j+1]]==v[idx[i]]: j+=1
            for k in range(i,j+1): r[idx[k]]=(i+j)/2+1
            i=j+1
        return r
    rx,ry=ranks(xs),ranks(ys)
    mx,my=sum(rx)/n,sum(ry)/n
    num=sum((a-mx)*(b-my) for a,b in zip(rx,ry)); den=(sum((a-mx)**2 for a in rx)*sum((b-my)**2 for b in ry))**0.5
    return num/den if den else None
rows=collections.defaultdict(lambda:collections.defaultdict(list))  # system -> dt -> rows
for f in glob.glob(f"{S}/s3/analysis/grades/**/grades.ndjson", recursive=True):
    m=re.search(r"dt=(\d{4}-\d{2}-\d{2}).*system=([^/]+)", f)
    if not m: continue
    dt,sysm=m.group(1),m.group(2)
    for line in open(f):
        line=line.strip()
        if not line: continue
        try: r=json.loads(line)
        except: continue
        rows[sysm][dt].append(r)
grades={}
for sysm,days in rows.items():
    def summarize(sel):
        sp=[]; ae=[]; bias=[]; pb=[]; hb=[]; buckets=collections.defaultdict(list)
        for dt in sel:
            rs=days[dt]
            x=[r["projected"] for r in rs]; y=[r["actual"] for r in rs]
            s=spearman(x,y)
            if s is not None: sp.append(s)
            for r in rs:
                ae.append(abs(r["projected"]-r["actual"])); bias.append(r["projected"]-r["actual"])
                (pb if r.get("is_pitcher") else hb).append(r["projected"]-r["actual"])
                buckets[r.get("bucket","?")].append(r["projected"]-r["actual"])
        return {"days":len(sel),"rows":len(ae),"spearman_mean":round(statistics.mean(sp),3) if sp else None,"spearman_median":round(statistics.median(sp),3) if sp else None,
                "mae":round(statistics.mean(ae),2) if ae else None,"bias":round(statistics.mean(bias),2) if bias else None,
                "bias_hitters":round(statistics.mean(hb),2) if hb else None,"bias_pitchers":round(statistics.mean(pb),2) if pb else None,
                "bias_by_bucket":{b:round(statistics.mean(v),2) for b,v in sorted(buckets.items())}}
    alld=sorted(days); pre=[d for d in alld if d<"2026-08-21"]; post=[d for d in alld if d>="2026-08-21" and d<="2026-09-13"]
    grades[sysm]={"all":summarize(alld),"pre_0821":summarize(pre),"post_0821_to_0913":summarize(post)}
out["grades"]=grades
# ---------- 6. Snapshots: GS gate since 08-19 ----------
gs={"days":0,"suppressed_starts":0,"suppressed_pts":0.0,"starter_days":0,"per_day":[]}
for f in sorted(glob.glob(f"{S}/s3/backtest/**/snapshots/*.json", recursive=True)):
    dt=os.path.basename(f)[:10]
    if dt<"2026-08-19" or dt>"2026-09-13": continue
    try: d=json.load(open(f))
    except: continue
    sup=[p for p in d.get("pitchers",[]) if p.get("gs_suppressed")]
    st=[p for p in d.get("pitchers",[]) if p.get("is_starter")]
    gs["days"]+=1; gs["suppressed_starts"]+=len(sup); gs["suppressed_pts"]+=sum(p.get("proj_pts_per_game",0) for p in sup); gs["starter_days"]+=len(st)
    if sup: gs["per_day"].append((dt,[(p["name"],round(p.get("proj_pts_per_game",0),1)) for p in sup]))
gs["suppressed_pts"]=round(gs["suppressed_pts"],1); out["gs_gate"]=gs
# ---------- 7. lineup gaps (Jon, 06-16..) ----------
gap=collections.defaultdict(lambda:[0.0,0.0,0])
for f in glob.glob(f"{S}/s3/analysis/lineup-gaps/**/gaps.ndjson", recursive=True):
    for line in open(f):
        try: r=json.loads(line)
        except: continue
        m=r["dt"][:7]; gap[m][0]+=r["actual_pts"]; gap[m][1]+=r["optimal_pts"]; gap[m][2]+=1
out["lineup_gap_by_month"]={m:{"days":v[2],"actual":round(v[0]),"optimal":round(v[1]),"eff":round(v[0]/v[1],3) if v[1] else None} for m,v in sorted(gap.items())}
json.dump(out, open(f"{S}/analysis.json","w"), indent=1, default=str)
print(json.dumps(out, indent=1, default=str)[:9000])
