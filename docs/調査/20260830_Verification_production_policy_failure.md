# Verification production policy failure

## Failure

Productionの`verification`設定が欠落し、pipelineとViewer report APIがdisabledになった。

## Problem

CORE、L1 Evidence、report store、Viewer routeは実装済みだったが、active policyがゼロ値のため
`/viewer/verification/summary`はHTTP 503となり、実応答からreportを生成するE2Eを証明できなかった。

## Cause

有効化時のpolicy値とdurable report pathをConfig validationが拘束せず、production設定で
section欠落とbackup境界外の既定pathを検出できなかった。

## Lesson

機能実装とtyped unavailableだけではproduction readinessにならない。active policy、durable store、
実応答、Viewer receiptを一つの経路として検証する必要がある。

## Invariant

Verificationを有効化する場合、modeとlevelは既知の値、report pathは絶対pathであり、backup設定時は
`backup.core_source`配下でなければならない。

## Enforcement

CORE Config validationが起動前にInvariantを検査し、違反時はfail closedする。

## Tests

Config unit testで有効値、未知mode、未知level、相対path、backup境界外pathを検査する。配備後は
正規Chat応答からreportが生成され、Viewer recent／summaryがHTTP 200で同じreportを返すことを確認する。
