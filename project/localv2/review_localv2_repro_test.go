package localv2

import (
 "context"
 "encoding/json"
 "errors"
 "os"
 "path/filepath"
 "strings"
 "testing"

 d "github.com/CutlyLab/Cutly/core/documentv2"
 v1 "github.com/CutlyLab/Cutly/core/videoedit"
 legacy "github.com/CutlyLab/Cutly/project/local"
)

func TestReviewMetadataNamedOperations(t *testing.T) {
 doc:=baseDocument()
 doc.Metadata["blob"]=json.RawMessage(`{"operations":[`+strings.Repeat(`0,`,4096)+`0]}`)
 if ds:=d.Validate(doc); d.HasErrorDiagnostics(ds) { t.Fatalf("document invalid: %+v",ds) }
 _,err:=CanonicalDocument(doc)
 t.Logf("CanonicalDocument(valid 4097-entry open metadata array): %v",err)
 if err!=nil { t.Fatalf("valid bounded open metadata rejected: %v",err) }
}

func TestReviewV1CreateOverV2CreateIntent(t *testing.T) {
 ctx:=context.Background()
 dir:=projectDir(t)
 s,err:=configure(dir,Options{})
 if err!=nil {t.Fatal(err)}
 raw,err:=CanonicalDocument(baseDocument()); if err!=nil {t.Fatal(err)}
 s.fault=func(point string)error { if point=="before_publish:"+DocumentName { return errors.New("crash before first projection") }; return nil }
 err=s.locked(ctx,true,func(x *session)error{return x.transition(ctx,diskState{Version:Version,DocumentID:baseDocument().ID,DocumentHash:digest(raw),Plans:[]PlanSummary{},History:[]historyRef{}},raw)})
 if !errors.Is(err,ErrCommitUnknown){t.Fatal(err)}
 if _,err:=os.Stat(filepath.Join(dir,DocumentName)); !errors.Is(err,os.ErrNotExist){t.Fatal(err)}
 oldDoc:=v1.Document{SchemaVersion:1,ID:"legacy"}
 _,err=legacy.Create(ctx,dir,oldDoc,legacy.Options{})
 t.Logf("v1.Create over durable v2 initialization intent: %v",err)
 _,reopenErr:=New(dir,Options{})
 t.Logf("v2 reopen after v1.Create: %v",reopenErr)
 if err==nil {t.Fatalf("v1 Create admitted reserved v2 root; v2 recovery now fails: %v",reopenErr)}
}
