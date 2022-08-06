## visit_historyにインデックスを貼る

created_atまで入れておくと、Covering Indexになり高速。
```
alter table visit_history drop index tenant_id_idx;
alter table visit_history add index visit_history_idx (tenant_id, competition_id, player_id, created_at );
show index from visit_history;
```