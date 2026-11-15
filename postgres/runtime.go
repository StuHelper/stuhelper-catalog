package postgres

import (
	"context"
	catalog "github.com/StuHelper/stuhelper-catalog"
)

// CheckRuntime rejects migration identities and privileges crossing module schemas.
func (r *Repository) CheckRuntime(ctx context.Context) error {
	var unsafe bool
	err := r.DB.QueryRowContext(ctx, `SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls
 OR has_schema_privilege(current_user,'catalog','CREATE')
 OR has_database_privilege(current_user,current_database(),'CREATE')
 OR pg_has_role(current_user,'pg_read_all_data','MEMBER')
 OR pg_has_role(current_user,'pg_write_all_data','MEMBER')
 OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('catalog','public','information_schema') AND nspname NOT LIKE 'pg_%' AND has_schema_privilege(current_user,oid,'USAGE'))
 FROM pg_roles WHERE rolname=current_user`).Scan(&unsafe)
	if err != nil || unsafe {
		return catalog.ErrUnavailable
	}
	if r.RequireTeacherDirectory {
		var ready bool
		err = r.DB.QueryRowContext(ctx, `SELECT has_table_privilege(current_user,'catalog.teacher_directory','SELECT')
 AND has_table_privilege(current_user,'catalog.teacher_site_sources','SELECT')
 AND has_table_privilege(current_user,'catalog.teacher_refresh_runs','SELECT,INSERT,UPDATE')
 AND has_table_privilege(current_user,'catalog.teacher_unmatched_sites','SELECT,INSERT,DELETE')
 AND has_table_privilege(current_user,'catalog.teacher_unmatched_snapshots','SELECT,INSERT,UPDATE')
 AND has_sequence_privilege(current_user,pg_get_serial_sequence('catalog.teacher_refresh_runs','position'),'USAGE')`).Scan(&ready)
		if err != nil || !ready {
			return catalog.ErrUnavailable
		}
	}
	if r.RequireDirectoryProjection {
		var ready bool
		err = r.DB.QueryRowContext(ctx, `SELECT bool_and(has_table_privilege(current_user,'catalog.'||name,'SELECT,INSERT,UPDATE,DELETE'))
 FROM unnest(ARRAY['term_orders','projection_generations','projection_heads','projection_failures','projection_imports','projection_objects','projection_variants','projection_relations','projection_teaching_history','projection_options','projection_aliases','projection_summaries','projection_documents','projection_selections']) name`).Scan(&ready)
		if err != nil || !ready {
			return catalog.ErrUnavailable
		}
		if err = r.DB.QueryRowContext(ctx, `SELECT has_function_privilege(current_user,'catalog.analyze_directory_projection()','EXECUTE')`).Scan(&ready); err != nil || !ready {
			return catalog.ErrUnavailable
		}
	}
	return nil
}
