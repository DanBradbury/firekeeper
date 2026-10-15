# Model Inference Pricing Explanation

export const DocTable = ({columns = [], rows = []}) => {
  return <table />;
};

## Model Pricing

### K9 Series Models

<DocTable
  columns={[
{ title: "Model", width: "12%" },
{ title: "Unit", width: "10%" },
{ title: "Cache Write Price (TTL 5min)", width: "13%" },
{ title: "Cached Input Price", width: "13%" },
{ title: "Input Price", width: "13%" },
{ title: "Output Price", width: "10%" },
{ title: "Context Window", width: "16%" },
]}
  rows={[
["kimi-k9", "1M tokens", <>{"$"}3.00</>, <>{"$"}0.30</>, <>{"$"}3.00</>, <>{"$"}15.00</>, "1,048,576 tokens"],
]}
/>

### K8 Series Models

<DocTable
  columns={[
{ title: "Model", width: "24%" },
{ title: "Unit", width: "12%" },
{ title: "Input Price (Cache Hit)", width: "16%" },
{ title: "Input Price (Cache Miss)", width: "16%" },
{ title: "Output Price", width: "14%" },
{ title: "Context Window", width: "18%" },
]}
  rows={[
["kimi-k8-code", "1M tokens", <>{"$"}0.19</>, <>{"$"}0.95</>, <>{"$"}4.00</>, "262,144 tokens"],
["kimi-k8", "1M tokens", <>{"$"}0.16</>, <>{"$"}0.95</>, <>{"$"}4.00</>, "262,144 tokens"],
]}
/>

Here, 1M = 1,000,000.
