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
const assert=require('node:assert/strict');
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
`
	if output, err := exec.Command(node, "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("browser interaction render: %v\n%s", err, output)
	}
}
