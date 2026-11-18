package remote

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBrowserPreservesPendingQuestionAcrossSnapshots(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	_, functions, _ := strings.Cut(indexHTML, "function renderInteractions(")
	functions, _, _ = strings.Cut(functions, "async function downloadExport(")
	if functions == "" {
		t.Fatal("browser interaction functions missing")
	}
	harness := `
const assert=require('assert').strict;
let focused, active='main', selectedInteraction='', attentionKey='', closed=false;
function element(){
  return {children:[], value:'', style:{}, classList:{toggle(){}},
    get firstChild(){return this.children[0]},
    append(...nodes){this.children.push(...nodes)},
    replaceChildren(...nodes){this.children=nodes},
    setAttribute(){}, focus(){focused=this},
    querySelector(tag){return this.children.find(child=>child.tag===tag)},
  };
}
const attention=element(),interaction=element(),interactionForms=new Map();
const document={createElement(tag){return {...element(),tag}}};
const agents=()=>[{id:'main',name:'Main'},{id:'review',name:'Reviewer'}];
const question={id:'q1',agent_id:'review',kind:'questions',payload:[{Text:'Which branch?'}]};
function selectAgent(id,requestID){active=id;selectedInteraction=requestID;renderInteractions([question])}
` + "function renderInteractions(" + functions + `
renderInteractions([question]);
assert.equal(interaction.firstChild,undefined);
assert.equal(attention.firstChild.textContent,'Reviewer needs input');
attention.firstChild.onclick();
const panel=interaction.firstChild;
assert.equal(panel.children[0].textContent,'Reviewer needs input');
const field=panel.children[1].children[1];
field.value='feature/review';field.focus();
renderInteractions([{...question}]);
assert.equal(interaction.firstChild,panel);
assert.equal(focused,field);
assert.equal(field.value,'feature/review');
active='main';
renderInteractions([question]);
assert.equal(interaction.firstChild,undefined);
attention.firstChild.onclick();
assert.equal(interaction.firstChild,panel);
assert.equal(field.value,'feature/review');
renderInteractions([]);
assert.equal(interactionForms.size,0);
assert.equal(attention.firstChild,undefined);
const optionsPanel=element();
const answer=questionField(optionsPanel,{Text:'Which branch?',Options:['main','dev'],AllowCustom:true},'options');
const select=optionsPanel.children[0].children[1];
const custom=optionsPanel.children[0].children[2];
assert.equal(answer(),'');
select.value='0';
assert.equal(answer(),'main');
select.value='custom';
custom.value='feature/review';
assert.equal(answer(),'');
select.onchange();
assert.equal(answer(),'feature/review');
`
	if output, err := exec.Command(node, "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("browser interaction render: %v\n%s", err, output)
	}
}

func TestBrowserSettingsSelectorKeepsDraftTypedDuringCatalogLoad(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	_, functions, _ := strings.Cut(indexHTML, "function clearComposerLine(")
	functions, _, _ = strings.Cut(functions, "function handleSettingsLine(")
	if functions == "" {
		t.Fatal("browser setting functions missing")
	}
	harness := `
const assert=require('assert').strict;
const element=()=>({children:[],dataset:{},value:'',focus(){},
  append(...nodes){this.children.push(...nodes)},replaceChildren(...nodes){this.children=nodes}});
const command=element(),input=element(),status=element(),drafts={};
const document={createElement:element};
let active='review',promptNotice='',releaseCatalog;
const snapshot={runtime:{agents:[{id:'review',name:'Review',model:'model-a'}]}};
const requests=[];
const schedule=()=>{};
async function apiFetch(path, options){
  requests.push([path,options.body]);
  if(path==='api/v1/catalog?agent_id=review'){
    await new Promise(resolve=>{releaseCatalog=resolve});
    return {ok:true,json:async()=>({models:['model-a','model-b']})};
  }
  return {ok:true};
}
` + "function clearComposerLine(" + functions + `
(async()=>{
  input.value='/model';
  const opening=openCatalogSelector('model','/model');
  input.value='Continue reviewing the diff';
  releaseCatalog();
  await opening;
  assert.equal(input.value,'Continue reviewing the diff');
  command.children[3].children[1].onclick();
  command.children[4].children[0].onclick();
  await Promise.resolve();
  assert.equal(input.value,'Continue reviewing the diff');
  assert.deepEqual(requests[1],['api/v1/agents/review/settings',JSON.stringify({model:'model-b',thinking:''})]);
  input.value='/model model-a';
  await submitSettings('review',{model:'model-a'},'/model model-a');
  assert.equal(input.value,'');
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	if output, err := exec.Command(node, "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("browser settings selector: %v\n%s", err, output)
	}
}
