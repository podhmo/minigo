from pathlib import Path
import runpy

base=Path(__file__).parent
setup=base/'setup.py'
text=setup.read_text().replace('codec-v1;minigo-9b0c47da','codec-v3;minigo-9b0c47da')
setup.write_text(text)
values=runpy.run_path(str(setup))
fields=values['fields']
p=base/'goroot/src/text/template/parse/experiment_snapshot.go'
text=p.read_text().replace('"encoding/json"; "fmt"; "sort"','"fmt"; "sort"; "strconv"; "strings"')
text=text.replace('return json.Marshal(b.Snapshot)','return experimentEncode(b.Snapshot),nil')
text=text.replace('var s experimentSnapshot; if err=json.Unmarshal(data,&s); err!=nil { return nil,err }','s:=experimentDecode(data)')
wire='''
// Compact length-prefixed wire format; all scalar parsing is host strconv.
type experimentWriter struct { B strings.Builder }
func(w *experimentWriter) num(v int64){ w.B.WriteString(strconv.FormatInt(v,10)); w.B.WriteByte('\n') }
func(w *experimentWriter) unum(v uint64){ w.B.WriteString(strconv.FormatUint(v,10)); w.B.WriteByte('\n') }
func(w *experimentWriter) boolean(v bool){ if v { w.num(1) } else { w.num(0) } }
func(w *experimentWriter) floating(v float64){ w.raw(strconv.FormatFloat(v,'g',-1,64)) }
func(w *experimentWriter) raw(v string){ w.num(int64(len(v))); w.B.WriteString(v) }
func(w *experimentWriter) ints(v []int){ if v==nil {w.num(-1);return};w.num(int64(len(v)));w.num(int64(cap(v)));for _,x:=range v {w.num(int64(x))} }
func(w *experimentWriter) strings(v []string){ if v==nil {w.num(-1);return};w.num(int64(len(v)));w.num(int64(cap(v)));for _,x:=range v {w.raw(x)} }
type experimentReader struct { Data string; Pos int }
func(r *experimentReader) line() string { n:=strings.IndexByte(r.Data[r.Pos:],'\n'); if n<0 {panic("truncated scalar")}; v:=r.Data[r.Pos:r.Pos+n];r.Pos+=n+1;return v }
func(r *experimentReader) num() int64 {v,e:=strconv.ParseInt(r.line(),10,64);if e!=nil{panic(e)};return v}
func(r *experimentReader) unum() uint64 {v,e:=strconv.ParseUint(r.line(),10,64);if e!=nil{panic(e)};return v}
func(r *experimentReader) boolean()bool{v:=r.num();if v!=0&&v!=1{panic("invalid boolean")};return v==1}
func(r *experimentReader) count()int{n:=r.num();if n<0||n>int64(len(r.Data)){panic("invalid count")};return int(n)}
func(r *experimentReader) raw() string {n:=r.count();if n>len(r.Data)-r.Pos{panic("truncated string")};v:=r.Data[r.Pos:r.Pos+n];r.Pos+=n;return v}
func(r *experimentReader) floating()float64{v,e:=strconv.ParseFloat(r.raw(),64);if e!=nil{panic(e)};return v}
func(r *experimentReader) ints()[]int {n:=r.num();if n==-1{return nil};if n<0||n>int64(len(r.Data)){panic("invalid ids")};capacity:=r.count();xs:=make([]int,int(n),capacity);for i:=range xs{xs[i]=int(r.num())};return xs}
func(r *experimentReader) strings()[]string {n:=r.num();if n==-1{return nil};if n<0||n>int64(len(r.Data)){panic("invalid strings")};capacity:=r.count();xs:=make([]string,int(n),capacity);for i:=range xs{xs[i]=r.raw()};return xs}
func experimentEncode(s experimentSnapshot)[]byte{
 w:=experimentWriter{};w.num(3)
 names:=make([]string,0,len(s.Names));for name:=range s.Names{names=append(names,name)};sort.Strings(names)
 w.num(int64(len(names)));for _,name:=range names{w.raw(name);w.num(int64(s.Names[name]))}
 w.num(int64(len(s.Trees)));for _,t:=range s.Trees{w.raw(t.Name);w.raw(t.ParseName);w.raw(string(t.Text));w.num(int64(t.Mode));w.raw(t.Left);w.raw(t.Right);w.num(int64(t.Root))}
 w.num(int64(len(s.Nodes)));for i,n:=range s.Nodes{if i==0{continue};w.num(int64(n.Kind));w.num(int64(n.Pos));w.num(int64(n.Tree));switch n.Kind{
'''
for kind,fs in fields.items():
    wire+=f'case Node{kind}:\n'
    for name,typ in fs:
        store='Identifier' if kind=='Identifier' else name
        arg=f'n.{store}'
        if typ=='complex': wire+='w.floating(n.Real);w.floating(n.Imag)\n';continue
        fn={'bool':'boolean','bytes':'raw','bytestring':'raw','string':'raw','strings':'strings','nodes':'ints','variables':'ints','commands':'ints','float64':'floating','uint64':'unum'}.get(typ,'num')
        if typ in ['bytes','bytestring']:arg=f'string({arg})'
        if fn=='num':arg=f'int64({arg})'
        wire+=f'w.{fn}({arg})\n'
        if typ=='bytes': wire+=f'w.num(int64(cap(n.{store})))\n'
wire+='''default:panic("unsupported wire node")}}
 return []byte(w.B.String())
}
func experimentDecode(data []byte)experimentSnapshot{
 r:=experimentReader{Data:string(data)};if r.num()!=3{panic("unsupported wire version")}
 s:=experimentSnapshot{Version:1,Names:map[string]int{}}
 for n:=r.count();n>0;n--{name:=r.raw();s.Names[name]=int(r.num())}
 s.Trees=make([]experimentTree,r.count());for i:=range s.Trees{s.Trees[i]=experimentTree{Name:r.raw(),ParseName:r.raw(),Text:[]byte(r.raw()),Mode:Mode(r.num()),Left:r.raw(),Right:r.raw(),Root:int(r.num())}}
 s.Nodes=make([]experimentNode,r.count());for i:=1;i<len(s.Nodes);i++{n:=experimentNode{Kind:NodeType(r.num()),Pos:Pos(r.num()),Tree:int(r.num())};switch n.Kind{
'''
for kind,fs in fields.items():
    wire+=f'case Node{kind}:\n'
    for name,typ in fs:
        store='Identifier' if kind=='Identifier' else name
        if typ=='complex':wire+='n.Real=r.floating();n.Imag=r.floating()\n';continue
        fn={'bool':'boolean','bytes':'raw','bytestring':'raw','string':'raw','strings':'strings','nodes':'ints','variables':'ints','commands':'ints','float64':'floating','uint64':'unum'}.get(typ,'num')
        val=f'r.{fn}()'
        if typ in ['bytes','bytestring']:val=f'[]byte({val})'
        if fn=='num' and typ not in ['int64']:val=f'int({val})'
        if typ=='bytes':
            wire+=f'raw:=r.raw();capacity:=r.count();n.{store}=make([]byte,len(raw),capacity);copy(n.{store},raw)\n'
        else: wire+=f'n.{store}={val}\n'
wire+='''default:panic("unsupported wire node")};s.Nodes[i]=n}
 if r.Pos!=len(r.Data){panic("trailing wire data")};return s
}
'''
# Python triple-quoted literals contain real newlines in Go rune literals.
wire=wire.replace("'\n'", "'\\n'")
p.write_text(text+wire)
template=(base/'cache_template.go.in').read_text().replace('VERSION_PLACEHOLDER',(base/'version.txt').read_text().strip())
(base/'goroot/src/text/template/experiment_cache.go').write_text(template)
