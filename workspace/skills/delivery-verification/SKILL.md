---
name: delivery-verification
description: Verify that a delivered change is connected from canonical source through artifact, owner route, actual actor, visible result, and receipt.
version: 1.0.0
---

# Delivery Verification

## Purpose

このカードは、sourceやunit testの成功を運用成果と取り違えず、配備・稼働・実利用の証拠鎖を検証する。検証Skillはdeploy、restart、credential変更を行わない。

## Inputs

- requested delivery and acceptance criteria
- source revision, build artifact, installed binary, active config, and service owner
- readiness, actual route I/O, user-visible result, logs, storage, and backup evidence

## Procedure

1. canonical source、artifact、installed binary、active configをSHA/identityで照合する。
2. service/listener owner、process generation、socket destination、readinessを確認する。
3. 本番同等の認証・policy・owner route・実Actorから必要なrequestを行う。
4. first/final content、stream/tool/error contract、visible result、logs、receiptを照合する。
5. `passed`、`failed`、`blocked`、`unverified` を保証単位で確定する。

## Output contract

`delivery_receipt` は source、artifact、installed、config、owner、readiness、route_request、visible_result、logs、storage、backup、status、failure_boundary を含む。

## Stop conditions

- sourceと稼働成果物の対応が取れない。
- health/listenerだけで実requestを代用しようとしている。
- canonical routeが使えず、direct backendやfake endpointへ切り替える必要がある。
- restart/deployなど未許可の外部変更が必要。
